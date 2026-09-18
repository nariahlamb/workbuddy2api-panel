package server

import (
	"bufio"
	"io"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// sseReader 按行读取上游 SSE，返回每个 data: 帧的载荷。
type sseReader struct {
	br *bufio.Reader
}

func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{br: bufio.NewReaderSize(r, 64*1024)}
}

// next 返回下一个 data 帧的载荷；流结束返回 io.EOF。
// 非 data 行（注释、event 行、空行）被跳过。
func (s *sseReader) next() (string, error) {
	for {
		line, err := s.br.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" {
				return "", io.EOF
			}
			if err != io.EOF {
				return "", err
			}
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: "), nil
		}
		if err == io.EOF {
			return "", io.EOF
		}
	}
}

// aggregateChat 复用 upstream 的聚合实现，把上游 SSE 聚合成完整 chat 响应对象。
func aggregateChat(r io.Reader) (map[string]any, error) {
	return upstream.Aggregate(r)
}
