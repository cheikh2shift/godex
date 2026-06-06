package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

const (
	huggingfaceAPI = "https://huggingface.co/api"
)

type hfRepoInfo struct {
	Siblings []struct {
		RFilename string `json:"rfilename"`
	} `json:"siblings"`
}

func getGGUFFiles(ctx context.Context, modelID string) ([]string, error) {
	url := fmt.Sprintf("%s/models/%s", huggingfaceAPI, modelID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get model info: status %d", resp.StatusCode)
	}

	var info hfRepoInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	var ggufFiles []string
	for _, s := range info.Siblings {
		if strings.HasSuffix(strings.ToLower(s.RFilename), ".gguf") {
			ggufFiles = append(ggufFiles, s.RFilename)
		}
	}

	return ggufFiles, nil
}

func selectBestGGUF(files []string) string {
	preferred := []string{"Q4_K_M", "Q5_K_M", "Q5_K_S", "Q4_0", "Q4_K_S", "Q3_K_M", "Q2_K", "F16", "F32"}

	for _, pref := range preferred {
		for _, f := range files {
			if strings.Contains(strings.ToUpper(f), pref) {
				return f
			}
		}
	}

	for _, f := range files {
		lower := strings.ToLower(f)
		if strings.Contains(lower, "q4") || strings.Contains(lower, "q5") || strings.Contains(lower, "q3") || strings.Contains(lower, "q2") {
			return f
		}
	}

	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f), ".gguf") {
			return f
		}
	}

	return ""
}

func getDownloadURL(modelID, filename string) string {
	return fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", modelID, filename)
}

type DownloadProgress struct {
	Filename   string
	Downloaded int64
	Total      int64
}

var quantOrder = map[string]int{
	"Q2_K":   1,
	"Q3_K_S": 2,
	"Q3_K_M": 3,
	"Q4_0":   4,
	"Q4_K_S": 5,
	"Q4_K_M": 6,
	"Q5_0":   7,
	"Q5_K_S": 8,
	"Q5_K_M": 9,
	"Q6_K":   10,
	"Q8_0":   11,
	"F16":    12,
	"F32":    13,
}

func SortQuantizations(quants []string) []string {
	sorted := make([]string, len(quants))
	copy(sorted, quants)

	slices.SortFunc(sorted, func(a, b string) int {
		orderA, okA := quantOrder[a]
		orderB, okB := quantOrder[b]
		if !okA && !okB {
			return strings.Compare(a, b)
		}
		if !okA {
			return 1
		}
		if !okB {
			return -1
		}
		return orderA - orderB
	})

	return sorted
}

func SortQuantizationsKeys(quantToFile map[string]string) []string {
	keys := make([]string, 0, len(quantToFile))
	for k := range quantToFile {
		keys = append(keys, k)
	}
	return SortQuantizations(keys)
}

func GetQuantizationDescription(quant string) string {
	descriptions := map[string]string{
		"Q2_K":   "Lowest quality, smallest size (~3GB for 7B)",
		"Q3_K_S": "Very low quality (~4GB for 7B)",
		"Q3_K_M": "Low quality (~5GB for 7B)",
		"Q4_0":   "Legacy format, not recommended",
		"Q4_K_S": "Good quality, smaller size (~4GB for 7B)",
		"Q4_K_M": "Best quality/size ratio (Recommended, ~5GB for 7B)",
		"Q5_0":   "Legacy format",
		"Q5_K_S": "High quality (~6GB for 7B)",
		"Q5_K_M": "Very high quality (~6.5GB for 7B)",
		"Q6_K":   "Near full quality (~8GB for 7B)",
		"Q8_0":   "Full quality, large size (~10GB for 7B)",
		"F16":    "Full precision, very large (~14GB for 7B)",
		"F32":    "32-bit float, largest (~28GB for 7B)",
	}

	if desc, ok := descriptions[quant]; ok {
		return desc
	}
	return ""
}
