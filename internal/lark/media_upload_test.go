package lark

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUploadMediaSizeContract(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		size       int64
		wantError  bool
	}{
		{"empty file", "file", 0, true}, {"empty image", "image", 0, true},
		{"image boundary", "image", 10 << 20, false}, {"image too large", "image", (10 << 20) + 1, true},
		{"file boundary", "file", 30 << 20, false}, {"file too large", "file", (30 << 20) + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/fixture.bin"
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			err = f.Truncate(tc.size)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			uploads := 0
			client := NewClient("https://open.feishu.test", "app", "secret")
			client.TenantToken = "fixture-token"
			client.TenantTokenExpiresAt = time.Now().Add(time.Hour)
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				uploads++
				reader, err := r.MultipartReader()
				if err != nil {
					t.Fatal(err)
				}
				var size int64
				for {
					part, err := reader.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					n, err := io.Copy(io.Discard, part)
					if err != nil {
						t.Fatal(err)
					}
					if part.FormName() == "file" || part.FormName() == "image" {
						size = n
					}
				}
				if size != tc.size {
					t.Errorf("size=%d want=%d", size, tc.size)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"file_key":"key","image_key":"key"}}`))}, nil
			})}
			_, err = client.UploadMedia(context.Background(), path, tc.kind, "fixture.bin")
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%v", err, tc.wantError)
			}
			wantUploads := 1
			if tc.wantError {
				wantUploads = 0
			}
			if uploads != wantUploads {
				t.Fatal(fmt.Sprintf("uploads=%d want=%d", uploads, wantUploads))
			}
		})
	}
}
