package guardexec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func bytesReader(raw []byte) io.Reader { return bytes.NewReader(raw) }

// readLine reads one newline-terminated record of at most limit bytes.
func readLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
		if len(line)+len(chunk) > limit {
			return nil, fmt.Errorf("record exceeds %d bytes", limit)
		}
		line = append(line, chunk...)
		if !isPrefix {
			return line, nil
		}
	}
}

func writeRecord(writer io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = writer.Write(raw)
	return err
}
