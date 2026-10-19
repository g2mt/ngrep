package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var errInvalidCache = errors.New("invalid or incompatible cache entry")

type section struct {
	Start, End uint64
	Vector     []float32
}

// v0: big-endian u32 version, NUL-terminated embedding settings JSON,
// big-endian u32 dimensions, then records of u64 start, u64 exclusive end,
// and dimensions IEEE-754 binary32 scalars. Records continue until EOF.
func readV0(input io.Reader, expected embeddingSettings, fileSize int64) ([]section, error) {
	r := bufio.NewReaderSize(input, 64*1024+1)
	var version uint32
	if err := binary.Read(r, binary.BigEndian, &version); err != nil || version != 0 {
		return nil, errInvalidCache
	}
	metadata, err := r.ReadSlice(0)
	if err != nil || len(metadata) > 64*1024 {
		return nil, errInvalidCache
	}
	var got embeddingSettings
	decoder := json.NewDecoder(bytes.NewReader(metadata[:len(metadata)-1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil || got != expected {
		return nil, errInvalidCache
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errInvalidCache
	}
	var dimensions uint32
	if err := binary.Read(r, binary.BigEndian, &dimensions); err != nil || dimensions > maxDimensions {
		return nil, errInvalidCache
	}
	var result []section
	var previous uint64
	for {
		_, err := r.Peek(1)
		if err == io.EOF {
			return result, nil
		}
		if err != nil || dimensions == 0 {
			return nil, errInvalidCache
		}
		var s section
		if err := binary.Read(r, binary.BigEndian, &s.Start); err != nil {
			return nil, errInvalidCache
		}
		if err := binary.Read(r, binary.BigEndian, &s.End); err != nil {
			return nil, errInvalidCache
		}
		if fileSize < 0 || s.Start >= s.End || s.End > uint64(fileSize) || (len(result) > 0 && s.Start <= previous) {
			return nil, errInvalidCache
		}
		s.Vector = make([]float32, dimensions)
		if err := binary.Read(r, binary.BigEndian, s.Vector); err != nil || !validVector(s.Vector) {
			return nil, errInvalidCache
		}
		result = append(result, s)
		previous = s.Start
	}
}

func writeV0(output io.Writer, embedding embeddingSettings, sections []section) error {
	metadata, err := json.Marshal(embedding)
	if err != nil {
		return err
	}
	if len(metadata)+1 > 64*1024 {
		return fmt.Errorf("cache embedding metadata is too large")
	}
	dimensions := 0
	if len(sections) > 0 {
		dimensions = len(sections[0].Vector)
	}
	for i, s := range sections {
		if len(s.Vector) != dimensions || !validVector(s.Vector) || s.Start >= s.End || (i > 0 && s.Start <= sections[i-1].Start) {
			return errInvalidCache
		}
	}
	w := bufio.NewWriter(output)
	if err := binary.Write(w, binary.BigEndian, uint32(0)); err != nil {
		return err
	}
	if _, err := w.Write(metadata); err != nil {
		return err
	}
	if err := w.WriteByte(0); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint32(dimensions)); err != nil {
		return err
	}
	for _, s := range sections {
		if err := binary.Write(w, binary.BigEndian, s.Start); err != nil {
			return err
		}
		if err := binary.Write(w, binary.BigEndian, s.End); err != nil {
			return err
		}
		if err := binary.Write(w, binary.BigEndian, s.Vector); err != nil {
			return err
		}
	}
	return w.Flush()
}
