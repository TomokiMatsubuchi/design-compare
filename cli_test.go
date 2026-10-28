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

	figmaInline := `[{"id":"1","name":"card","x":0,"y":0,"w":400,"h":300}]`
	webInline := `[{"selector":"#card","x":0,"y":0,"w":400,"h":300}]`
	gotLayout, err := parseCLIArgs([]string{
		"--mode", "layout_tree",
		"--figma-layout", figmaInline,
		"--web-layout", webInline,
		"--figma-layout-file", "figma.json",
		"--web-layout-file", "web.json",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseCLIArgs layout: %v", err)
	}
	if gotLayout["figma_layout"] != figmaInline {
		t.Errorf("figma_layout=%v", gotLayout["figma_layout"])
	}
	if gotLayout["web_layout"] != webInline {
		t.Errorf("web_layout=%v", gotLayout["web_layout"])
	}
	if gotLayout["figma_layout_path"] != "figma.json" {
		t.Errorf("figma_layout_path=%v", gotLayout["figma_layout_path"])
	}
	if gotLayout["web_layout_path"] != "web.json" {
		t.Errorf("web_layout_path=%v", gotLayout["web_layout_path"])
	}
}

func TestParseCLIArgsRejectsUnexpectedArgs(t *testing.T) {
	var stderr bytes.Buffer
	_, err := parseCLIArgs([]string{
		"--mode", "strict",
		"--image-a", "a.png",
		"--image-b", "b.png",
		"--generate-diff", "false",
		"--max-diff-pixels", "100",
	}, &stderr)
	if err == nil {
		t.Fatal("expected error for space-separated boolean value")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unexpected argument(s): false") {
		t.Errorf("error should mention unexpected false, got %q", msg)
	}
	if !strings.Contains(msg, "--flag=value") {
		t.Errorf("error should hint --flag=value form, got %q", msg)
	}

	_, err = parseCLIArgs([]string{
		"--mode", "strict",
		"--image-a", "a.png",
		"--image-b", "b.png",
		"--include-aa", "false",
		"--ignore-region", "0,0,100,50",
	}, &stderr)
	if err == nil {
		t.Fatal("expected error for space-separated --include-aa value")
	}
	if !strings.Contains(err.Error(), "unexpected argument(s): false") {
		t.Errorf("error should mention unexpected false, got %q", err.Error())
	}

	got, err := parseCLIArgs([]string{
		"--mode", "strict",
		"--image-a", "a.png",
		"--image-b", "b.png",
		"--generate-diff=false",
		"--max-diff-pixels", "100",
	}, &stderr)
	if err != nil {
		t.Fatalf("equals form must succeed: %v", err)
	}
	if got["generate_diff"] != false {
		t.Errorf("generate_diff=%v", got["generate_diff"])
	}
	if got["max_diff_pixels"] != 100 {
		t.Errorf("max_diff_pixels=%v", got["max_diff_pixels"])
	}
}

func TestParseCLIArgsOmitsUnsetFlags(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseCLIArgs([]string{"--mode", "perceptual", "--image-a", "a.png", "--image-b", "b.png"}, &stderr)
	if err != nil {
		t.Fatalf("parseCLIArgs: %v", err)
	}
	for _, key := range []string{
		"threshold", "min_match", "pass_rate", "max_diff_pixels", "include_aa",
		"ignore_region", "generate_diff", "figma_layout", "web_layout",
		"figma_layout_path", "web_layout_path",
		"viewport_preset", "viewport_width", "viewport_height", "ignore_nodes",
		"count_extra_web", "max_details", "diff_on_mismatch",
	} {
		if _, ok := got[key]; ok {
			t.Errorf("unset flag %s should not be in args: %v", key, got)
		}
	}
}

func TestParseCLIArgsMapsViewportAndLayoutFlags(t *testing.T) {
	var stderr bytes.Buffer
	got, err := parseCLIArgs([]string{
		"--mode", "layout_integrity",
		"--web-layout-file", "web.json",
		"--viewport-preset", "ipad_landscape",
		"--viewport-width", "1024",
		"--viewport-height", "768",
		"--ignore-nodes", ".ad-*,nav",
		"--count-extra-web",
		"--max-details", "5",
		"--diff-on-mismatch",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseCLIArgs: %v", err)
	}
	if got["mode"] != "layout_integrity" {
		t.Errorf("mode=%v", got["mode"])
	}
	if got["web_layout_path"] != "web.json" {
		t.Errorf("web_layout_path=%v", got["web_layout_path"])
	}
	if got["viewport_preset"] != "ipad_landscape" {
		t.Errorf("viewport_preset=%v", got["viewport_preset"])
	}
	if got["viewport_width"] != 1024.0 {
		t.Errorf("viewport_width=%v", got["viewport_width"])
	}
	if got["viewport_height"] != 768.0 {
		t.Errorf("viewport_height=%v", got["viewport_height"])
	}
	if got["ignore_nodes"] != ".ad-*,nav" {
		t.Errorf("ignore_nodes=%v", got["ignore_nodes"])
	}
	if got["count_extra_web"] != true {
		t.Errorf("count_extra_web=%v", got["count_extra_web"])
	}
	if got["max_details"] != 5 {
		t.Errorf("max_details=%v", got["max_details"])
	}
	if got["diff_on_mismatch"] != true {
		t.Errorf("diff_on_mismatch=%v", got["diff_on_mismatch"])
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

func TestRunCLIUnknownFlagOnce(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLIWithIO([]string{"--bogus"}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("expected exit 1, got %d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout must stay empty on parse error, got %s", stdout.String())
	}
	msg := "flag provided but not defined"
	if n := strings.Count(stderr.String(), msg); n != 1 {
		t.Errorf("stderr must mention %q exactly once, got %d in %q", msg, n, stderr.String())
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
