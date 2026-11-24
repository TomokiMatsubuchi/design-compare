package main

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// perceptual / strict の応答が generate_diff / diff_on_mismatch の実効値と、
// diff_image が空の理由 (details) を返すことを固定する。
func TestDiffImageEmptyReasonInResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.png")
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cases := []struct {
		name       string
		args       map[string]any
		wantGen    bool
		wantOnMis  bool
		wantReason string
	}{
		{"default", map[string]any{}, true, false, ""},
		{"generate_diff=false", map[string]any{"generate_diff": false}, false, false, "diff image not generated (generate_diff=false)"},
		{"diff_on_mismatch=true", map[string]any{"diff_on_mismatch": true}, true, true, "diff image omitted (diff_on_mismatch=true, status=success)"},
		{"both", map[string]any{"generate_diff": false, "diff_on_mismatch": true}, false, true, "diff image not generated (generate_diff=false)"},
	}
	for _, mode := range []string{"perceptual", "strict"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				args := map[string]any{"mode": mode, "image_path_a": path, "image_path_b": path}
				for k, v := range tc.args {
					args[k] = v
				}
				res, err := compareDesignHandler(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
				if err != nil {
					t.Fatalf("handler failed: %v", err)
				}
				var got map[string]interface{}
				if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &got); err != nil {
					t.Fatal(err)
				}
				if got["generate_diff"] != tc.wantGen || got["diff_on_mismatch"] != tc.wantOnMis {
					t.Errorf("generate_diff=%v diff_on_mismatch=%v, want %v %v", got["generate_diff"], got["diff_on_mismatch"], tc.wantGen, tc.wantOnMis)
				}
				details, _ := got["details"].([]interface{})
				found := 0
				for _, d := range details {
					if s, _ := d.(string); s == tc.wantReason {
						found++
					}
				}
				if tc.wantReason == "" {
					if got["diff_image"] == "" {
						t.Errorf("diff_image unexpectedly empty")
					}
					if len(details) > 0 && found != 0 {
						t.Errorf("unexpected reason in details: %v", details)
					}
					return
				}
				if found != 1 {
					t.Errorf("details %v should contain %q exactly once", details, tc.wantReason)
				}
			})
		}
	}
}
