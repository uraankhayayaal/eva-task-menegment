package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatermarkRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watermark.json")

	ts := time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC)
	if err := saveWatermark(path, ts); err != nil {
		t.Fatalf("saveWatermark: %v", err)
	}
	got, err := loadWatermark(path)
	if err != nil {
		t.Fatalf("loadWatermark: %v", err)
	}
	if !got.Equal(ts) {
		t.Errorf("watermark = %v, want %v", got, ts)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: %v", err)
	}
}

func TestLoadWatermarkMissing(t *testing.T) {
	got, err := loadWatermark(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("loadWatermark: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("watermark = %v, want zero", got)
	}
}