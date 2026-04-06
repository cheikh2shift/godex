package ml

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var DebugMode bool

type VisionResult struct {
	Response string  `json:"response"`
	Duration float64 `json:"duration_ms"`
}

var (
	visionServer     *exec.Cmd
	visionServerOnce sync.Once
	visionServerErr  error
	tesseractPath    string
)

func visionDebug(format string, args ...interface{}) {
	if DebugMode {
		log.Printf("[vision] "+format, args...)
	}
}

var PromptInstall func(string) bool

func EnsureOCR(ctx context.Context) error {
	tesseractPath, err := exec.LookPath("tesseract")
	if err == nil {
		visionDebug("Found tesseract at: %s", tesseractPath)
		return nil
	}

	visionDebug("Tesseract not found, prompting user...")

	if PromptInstall != nil && PromptInstall("tesseract-ocr (for reading text in images)") {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.CommandContext(ctx, "brew", "install", "tesseract")
		case "linux":
			cmd = exec.CommandContext(ctx, "sh", "-c", "sudo apt-get update && sudo apt-get install -y tesseract-ocr")
		}
		if cmd != nil {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}

		tesseractPath, err := exec.LookPath("tesseract")
		if err == nil {
			visionDebug("Found tesseract at: %s", tesseractPath)
			return nil
		}
	}

	return fmt.Errorf("tesseract not installed. Please run: sudo apt-get install -y tesseract-ocr")
}

func EnsurePDFTools(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "pdftotext", "-v")
	if err := cmd.Run(); err != nil {
		visionDebug("pdftotext not found, prompting user...")

		if PromptInstall != nil && PromptInstall("poppler-utils (for reading PDFs)") {
			var installCmd *exec.Cmd
			switch runtime.GOOS {
			case "darwin":
				installCmd = exec.CommandContext(ctx, "brew", "install", "poppler")
			case "linux":
				installCmd = exec.CommandContext(ctx, "sh", "-c", "sudo apt-get update && sudo apt-get install -y poppler-utils")
			}
			if installCmd != nil {
				installCmd.Stdout = os.Stdout
				installCmd.Stderr = os.Stderr
				installCmd.Run()
			}

			cmd := exec.CommandContext(ctx, "pdftotext", "-v")
			if cmd.Run() == nil {
				return nil
			}
		}

		return fmt.Errorf("pdftotext not installed. Run: sudo apt-get install -y poppler-utils")
	}
	return nil
}

func QueryImage(ctx context.Context, imagePath, prompt string) (string, error) {
	if err := EnsureOCR(ctx); err != nil {
		return "", err
	}

	visionDebug("Running OCR on: %s", imagePath)

	cmd := exec.CommandContext(ctx, "tesseract", imagePath, "stdout")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tesseract failed: %w - %s", err, stderr.String())
	}

	result := strings.TrimSpace(stdout.String())
	if result == "" {
		return "No text detected in image", nil
	}

	return result, nil
}

type pageRange struct {
	start int
	end   int
}

func parsePageRange(pages string) pageRange {
	pr := pageRange{}
	if pages == "" {
		return pr
	}

	parts := strings.Split(pages, "-")
	if len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &pr.start)
		fmt.Sscanf(parts[1], "%d", &pr.end)
	} else if len(parts) == 1 {
		fmt.Sscanf(parts[0], "%d", &pr.start)
		pr.end = pr.start
	}
	return pr
}

func ExtractPDF(ctx context.Context, pdfPath, pages string) (string, error) {
	if err := EnsurePDFTools(ctx); err != nil {
		return "", err
	}

	visionDebug("Extracting PDF: %s (pages: %s)", pdfPath, pages)

	args := []string{"-layout"}

	if pages != "" {
		parsed := parsePageRange(pages)
		if parsed.start > 0 {
			args = append(args, "-f", fmt.Sprintf("%d", parsed.start))
		}
		if parsed.end > 0 {
			args = append(args, "-l", fmt.Sprintf("%d", parsed.end))
		}
	}

	args = append(args, pdfPath, "-")

	cmd := exec.CommandContext(ctx, "pdftotext", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftotext failed: %w - %s", err, stderr.String())
	}

	result := strings.TrimSpace(stdout.String())
	if result == "" {
		return "No text detected in PDF", nil
	}

	return result, nil
}

func StopVisionServer() {
	if visionServer != nil && visionServer.Process != nil {
		visionServer.Process.Signal(os.Kill)
		visionServer = nil
		visionServerOnce = sync.Once{}
	}
}

func StartVisionServer(ctx context.Context) error {
	return EnsureOCR(ctx)
}

func checkServerHealth(url string) bool {
	return false
}

func getGodexBinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".godex", "bin"), nil
}

func DownloadVisionServer(ctx context.Context) error {
	visionDebug("Attempting to install tesseract...")

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "brew", "install", "tesseract")
	case "linux":
		cmd = exec.CommandContext(ctx, "sh", "-c", "sudo apt-get update && sudo apt-get install -y tesseract-ocr")
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		visionDebug("Auto-install failed: %v", err)
		return EnsureOCR(ctx)
	}

	return EnsureOCR(ctx)
}

func EnsureVisionModel(ctx context.Context) error {
	return EnsureOCR(ctx)
}
