package guardidentity

import (
	"bytes"
	"io"
)

func bytesReader(raw []byte) io.Reader { return bytes.NewReader(raw) }
