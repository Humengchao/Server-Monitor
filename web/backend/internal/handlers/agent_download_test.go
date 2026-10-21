package handlers

import (
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAgentDownloadStreamsRangesAndRefreshesChecksum(t *testing.T) {
	gin.SetMode(gin.TestMode)
	directory := t.TempDir()
	name := "server-monitor-agent-linux-amd64"
	file := filepath.Join(directory, name)
	if err := os.WriteFile(file, []byte("first binary content"), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewAgentHandler(nil, nil, "", directory)
	r := gin.New()
	r.GET("/download/:os/:arch", h.Download)
	request := func(query, rangeHeader string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", "/download/linux/amd64"+query, nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		return response
	}
	for _, content := range []string{"first binary content", "replacement binary bytes"} {
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			response := request("?checksum=1", "")
			if response.Code != 200 || response.Body.String() != fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(content)), name) {
				t.Fatalf("checksum: %d %s", response.Code, response.Body.String())
			}
		}
		response := request("", "bytes=2-7")
		if response.Code != 206 || response.Body.String() != content[2:8] || !strings.HasPrefix(response.Header().Get("Content-Range"), "bytes 2-7/") {
			t.Fatalf("range: %d %s", response.Code, response.Body.String())
		}
	}
}
