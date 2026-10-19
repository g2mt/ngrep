package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"golang.org/x/term"
)

type options struct {
	Key          bool   `short:"k" help:"Set the API key using a hidden terminal prompt."`
	Index        bool   `short:"i" help:"Index files without searching."`
	Force        bool   `short:"f" help:"Rebuild cached file embeddings."`
	Unrestricted int    `short:"u" type:"counter" help:"Relax filtering: -u ignores rules, -uu includes hidden files, -uuu includes binary files."`
	Hidden       bool   `short:"." help:"Include hidden files and directories."`
	Binary       bool   `help:"Include NUL-containing files (invalid UTF-8 is replaced for the API)."`
	Follow       bool   `short:"L" help:"Follow symbolic links without duplicates or cycles."`
	Top          int    `default:"10" help:"Maximum number of sections to return before merging."`
	Query        string `arg:"" optional:"" help:"Natural-language query (quote it if it contains spaces)."`
}

func main() {
	var opts options
	parser, err := kong.New(&opts, kong.Name("ng"), kong.Description("Search local files by meaning using OpenAI-compatible embeddings."))
	if err == nil {
		_, err = parser.Parse(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ng:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ui := newConsole(os.Stdin, os.Stdout, os.Stderr)
	if err := run(ctx, opts, ui); err != nil {
		ui.warn("ng: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options, ui *console) error {
	if opts.Top < 1 {
		return errors.New("--top must be at least 1")
	}
	if opts.Key && (opts.Index || opts.Query != "" || opts.Force || opts.Unrestricted != 0 || opts.Hidden || opts.Binary || opts.Follow) {
		return errors.New("--key cannot be combined with indexing or searching")
	}
	if !opts.Key && (opts.Index == (strings.TrimSpace(opts.Query) != "")) {
		return errors.New("provide either --index or a quoted search query")
	}
	dir, err := stateDirectory()
	if err != nil {
		return err
	}
	settings, err := loadSettings(dir)
	if err != nil {
		return err
	}
	if opts.Key {
		return setupKey(ctx, dir, ui)
	}
	key, err := os.ReadFile(filepath.Join(dir, "auth"))
	if err != nil {
		return fmt.Errorf("read API key (run ng --key first): %w", err)
	}
	client, err := newAPIClient(settings.Embedding, strings.TrimSpace(string(key)))
	if err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	privacy, err := newPrivacy(settings.Whitelist, ui, settings.Embedding.APIHost)
	if err != nil {
		return err
	}
	// An unrestricted flag can never bypass this check.
	if err := privacy.authorize(ctx, realRoot); err != nil {
		return err
	}
	files, err := discover(ctx, root, dir, opts, ui.warn)
	if err != nil {
		return err
	}
	cache := &fileCache{dir: filepath.Join(dir, "cache"), limit: settings.MaxFilesInCache, embedding: settings.Embedding}
	defer func() {
		if err := cache.evict(); err != nil {
			ui.warn("cache cleanup: %v", err)
		}
	}()
	idx := &indexer{cache: cache, api: client, chars: settings.EstimatedCharsPerSection, force: opts.Force,
		binary: opts.Binary || opts.Unrestricted >= 3, privacy: privacy, warn: ui.warn}
	var query []float32
	if !opts.Index && len(files) != 0 {
		query, err = client.embed(ctx, opts.Query)
		if err != nil {
			return err
		}
		idx.dimensions = len(query)
	}
	best := newTopSections(opts.Top)
	indexed := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		sections, info, skipped, err := idx.file(ctx, file)
		if err != nil {
			return err
		}
		if skipped {
			continue
		}
		indexed++
		if !opts.Index {
			for _, section := range sections {
				best.add(match{source: file, info: info, start: section.Start, end: section.End, score: cosine(query, section.Vector)})
			}
		}
	}
	if opts.Index {
		_, err = fmt.Fprintf(ui.err, "Indexed %d file(s).\n", indexed)
		return err
	}
	return printMatches(ui, root, best.sorted())
}

type console struct {
	in                 *os.File
	out, err           io.Writer
	outColor, errColor bool
	interactive        bool
}

func newConsole(in, out, stderr *os.File) *console {
	_, noColor := os.LookupEnv("NO_COLOR")
	color := !noColor && os.Getenv("TERM") != "dumb"
	return &console{in: in, out: out, err: stderr, interactive: term.IsTerminal(int(in.Fd())),
		outColor: color && term.IsTerminal(int(out.Fd())), errColor: color && term.IsTerminal(int(stderr.Fd()))}
}

func (ui *console) warn(format string, args ...any) {
	message := safeText(fmt.Sprintf(format, args...), false)
	if ui.errColor {
		message = "\x1b[31m" + message + "\x1b[0m"
	}
	fmt.Fprintln(ui.err, message)
}

func setupKey(ctx context.Context, dir string, ui *console) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !ui.interactive {
		return errors.New("API key setup requires an interactive terminal")
	}
	fmt.Fprint(ui.err, "Paste API key (input hidden): ")
	password, err := ui.readPrompt(ctx, true)
	fmt.Fprintln(ui.err)
	if err != nil {
		return fmt.Errorf("read API key: %w", err)
	}
	defer clear(password)
	if err := ctx.Err(); err != nil {
		return err
	}
	key := strings.TrimSpace(string(password))
	if err := validateKey(key); err != nil {
		return err
	}
	if err := atomicFile(filepath.Join(dir, "auth"), func(w io.Writer) error {
		_, err := io.WriteString(w, key+"\n")
		return err
	}); err != nil {
		return fmt.Errorf("save API key: %w", err)
	}
	fmt.Fprintln(ui.err, "API key saved.")
	return nil
}
