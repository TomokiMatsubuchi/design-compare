package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUseCLI(t *testing.T) {
	if useCLI([]string{"design-compare"}) {
		t.Fatal("no extra args must keep stdio MCP mode")
	}
	if !useCLI([]string{"design-compare", "--mode", "strict"}) {
		t.Fatal("extra args must select CLI mode")
	}
}

func TestParseCLIArgsMapsFlags(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseCLIArgs([]string{
		"--mode", "strict",
		"--image-a", "a.png",
		"--image-b", "b.png",
		"--threshold", "0.2",
		"--min-match", "99",
		"--max-diff-pixels", "3",
		"--include-aa",
		"--ignore-region", "0,0,1,1",
		"--generate-diff=false",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseCLIArgs: %v", err)
	}
	if got["mode"] != "strict" {
		t.Errorf("mode=%v", got["mode"])
	}
	if got["image_path_a"] != "a.png" || got["image_path_b"] != "b.png" {
		t.Errorf("image paths: %v", got)
	}
	if got["threshold"] != 0.2 {
		t.Errorf("threshold=%v", got["threshold"])
	}
	if got["min_match"] != 99.0 {
		t.Errorf("min_match=%v", got["min_match"])
	}
	if got["max_diff_pixels"] != 3 {
		t.Errorf("max_diff_pixels=%v", got["max_diff_pixels"])
	}
	if got["include_aa"] != true {
		t.Errorf("include_aa=%v", got["include_aa"])
	}
	if got["ignore_region"] != "0,0,1,1" {
		t.Errorf("ignore_region=%v", got["ignore_region"])
	}
	if got["generate_diff"] != false {
		t.Errorf("generate_diff=%v", got["generate_diff"])
	}
}

func TestParseCLIArgsOmitsUnsetFlags(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseCLIArgs([]string{"--mode", "perceptual", "--image-a", "a.png", "--image-b", "b.png"}, &stderr)
	if err != nil {
		t.Fatalf("parseCLIArgs: %v", err)
	}
	for _, key := range []string{"threshold", "min_match", "pass_rate", "max_diff_pixels", "include_aa", "ignore_region", "generate_diff", "figma_layout_path", "web_layout_path"} {
		if _, ok := got[key]; ok {
			t.Errorf("unset flag %s should not be in args: %v", key, got)
		}
	}
}

func TestParseCLIArgsHelp(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseCLIArgs([]string{"-h"}, &stderr)
	if err != flag.ErrHelp {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("help output missing Usage: %q", stderr.String())
	}
}

func TestRunCLIStrictMatchAndMismatch(t *testing.T) {
	tmp := t.TempDir()
	pathA := saveTempImage(t, tmp, "a.png", generateSolidImage(8, 8, color.White))
	pathB := saveTempImage(t, tmp, "b.png", generateSolidImage(8, 8, color.White))
	pathC := saveTempImage(t, tmp, "c.png", generateSolidImage(8, 8, color.Black))

	var stdout, stderr bytes.Buffer
	code := runCLIWithIO([]string{
		"--mode", "strict",
		"--image-a", pathA,
		"--image-b", pathB,
		"--generate-diff=false",
	}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("match exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if result["status"] != "success" || result["mode"] != "strict" {
		t.Errorf("unexpected result: %v", result)
	}

	stdout.Reset()
	stderr.Reset()
	code = runCLIWithIO([]string{
		"--mode", "strict",
		"--image-a", pathA,
		"--image-b", pathC,
		"--generate-diff=false",
	}, &stdout, &stderr)
	if code != exitMismatch {
		t.Fatalf("mismatch exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("mismatch stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if result["status"] != "mismatch" {
		t.Errorf("status=%v", result["status"])
	}
}

func TestRunCLILayoutTree(t *testing.T) {
	tmp := t.TempDir()
	figma := `[{"id":"1","name":"a","x":0,"y":0,"w":10,"h":10}]`
	web := `[{"selector":".a","x":0,"y":0,"w":10,"h":10}]`
	figmaPath := filepath.Join(tmp, "figma.json")
	webPath := filepath.Join(tmp, "web.json")
	if err := os.WriteFile(figmaPath, []byte(figma), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webPath, []byte(web), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runCLIWithIO([]string{
		"--mode", "layout_tree",
		"--figma-layout-file", figmaPath,
		"--web-layout-file", webPath,
	}, &stdout, &stderr)
	if code != exitSuccess {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if result["status"] != "success" || result["mode"] != "layout_tree" {
		t.Errorf("unexpected result: %v", result)
	}
}

func TestRunCLIErrorExit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLIWithIO([]string{"--mode", "strict"}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("expected exit 1, got %d stderr=%s", code, stderr.String())
	}
	// stdout を JSON としてパースする呼び出し側を壊さないよう、エラー時は
	// stdout を空のままエラー本文を stderr へ出す。
	if stdout.Len() != 0 {
		t.Errorf("stdout must stay empty on error, got %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Strict mode input error: either image_path_a or image_a_base64 is required") {
		t.Errorf("expected error text on stderr, got %q", stderr.String())
	}
}

func TestExitCodeFromResultJSON(t *testing.T) {
	if got := exitCodeFromResultJSON(`{"status":"success"}`); got != exitSuccess {
		t.Errorf("success -> %d", got)
	}
	if got := exitCodeFromResultJSON(`{"status":"mismatch"}`); got != exitMismatch {
		t.Errorf("mismatch -> %d", got)
	}
	if got := exitCodeFromResultJSON(`{"status":"skipped"}`); got != exitMismatch {
		t.Errorf("skipped -> %d", got)
	}
	if got := exitCodeFromResultJSON(`not json`); got != exitError {
		t.Errorf("invalid json -> %d", got)
	}
}
