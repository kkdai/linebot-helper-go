package loader

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

var extensionFormats = map[string]bool{
	"doc": true, "docx": true, "docm": true,
	"ppt": true, "pptx": true, "pptm": true,
	"xls": true, "xlsx": true, "xlsm": true,
	"odt": true, "ods": true, "odp": true,
	"rtf": true, "epub": true, "csv": true, "pdf": true,
}

var contentTypeFormats = map[string]string{
	"application/pdf":    "pdf",
	"application/msword": "doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   "docx",
	"application/vnd.ms-word.document.macroenabled.12":                          "docm",
	"application/vnd.ms-powerpoint":                                             "ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/vnd.ms-powerpoint.presentation.macroenabled.12":                "pptm",
	"application/vnd.ms-excel":                                                  "xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.ms-excel.sheet.macroenabled.12":                            "xlsm",
	"application/vnd.oasis.opendocument.text":                                   "odt",
	"application/vnd.oasis.opendocument.spreadsheet":                            "ods",
	"application/vnd.oasis.opendocument.presentation":                           "odp",
	"application/rtf":      "rtf",
	"text/rtf":             "rtf",
	"application/epub+zip": "epub",
	"text/csv":             "csv",
}

// DetectDocumentFormat checks the URL extension first, and if unknown, performs a HEAD request
// to inspect Content-Type (skipping PTT URLs which return 403 on HEAD).
func DetectDocumentFormat(ctx context.Context, rawURL string, client *http.Client) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		base := path.Base(u.Path)
		if idx := strings.LastIndex(base, "."); idx != -1 {
			ext := strings.ToLower(base[idx+1:])
			if extensionFormats[ext] {
				return ext
			}
		}
	}

	if IsPTTURL(rawURL) {
		return ""
	}

	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	headCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(headCtx, http.MethodHead, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9,ja;q=0.8,en-US;q=0.7,en;q=0.6")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("HEAD request failed checking document type", "url", rawURL, "error", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	ct := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	return contentTypeFormats[ct]
}

// LoadDocument downloads the document from rawURL and converts its contents to Markdown/text.
func LoadDocument(ctx context.Context, rawURL, docFormat string, client *http.Client) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9,ja;q=0.8,en-US;q=0.7,en;q=0.6")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", "over18=1")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to download document (%d): %s", resp.StatusCode, rawURL)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return ConvertDocumentBytes(data, docFormat)
}

// ConvertDocumentBytes converts document bytes into Markdown/text based on format.
func ConvertDocumentBytes(data []byte, docFormat string) (string, error) {
	switch strings.ToLower(docFormat) {
	case "pdf":
		text, _, err := ExtractTextFromPDFBytes(data)
		return text, err
	case "csv":
		return convertCSVToMarkdown(data)
	case "rtf":
		return convertRTFToText(data), nil
	case "docx", "docm", "pptx", "pptm", "xlsx", "xlsm", "odt", "ods", "odp", "epub":
		return extractTextFromZipXML(data)
	default:
		// Attempt ZIP/XML extraction or PDF extraction
		if text, err := extractTextFromZipXML(data); err == nil && strings.TrimSpace(text) != "" {
			return text, nil
		}
		if text, _, err := ExtractTextFromPDFBytes(data); err == nil && strings.TrimSpace(text) != "" {
			return text, nil
		}
		return "", fmt.Errorf("unsupported or unreadable document format: %s", docFormat)
	}
}

// ExtractTextFromPDFBytes extracts text from PDF bytes using ledongthuc/pdf.
func ExtractTextFromPDFBytes(data []byte) (string, int, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", 0, err
	}
	numPages := reader.NumPage()
	var pages []string
	for i := 1; i <= numPages; i++ {
		p := reader.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			pages = append(pages, trimmed)
		}
	}
	if len(pages) == 0 {
		return "", numPages, fmt.Errorf("no text content extracted from PDF")
	}
	return strings.Join(pages, "\n\n"), numPages, nil
}

func convertCSVToMarkdown(data []byte) (string, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return "", err
	}
	if len(records) == 0 {
		return "", fmt.Errorf("empty CSV")
	}
	var sb strings.Builder
	for i, row := range records {
		sb.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			sep := make([]string, len(row))
			for j := range sep {
				sep[j] = "---"
			}
			sb.WriteString("| " + strings.Join(sep, " | ") + " |\n")
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func convertRTFToText(data []byte) string {
	s := string(data)
	var out strings.Builder
	inControl := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' {
			inControl = true
			continue
		}
		if inControl {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' {
				continue
			}
			inControl = false
			if ch == ' ' {
				continue
			}
		}
		if ch == '{' || ch == '}' {
			continue
		}
		out.WriteByte(ch)
	}
	return strings.TrimSpace(out.String())
}

func extractTextFromZipXML(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var sections []string
	for _, f := range zr.File {
		name := strings.ToLower(f.Name)
		isContent := strings.HasPrefix(name, "word/document") ||
			strings.HasPrefix(name, "ppt/slides/slide") ||
			strings.HasPrefix(name, "xl/sharedstrings") ||
			strings.HasPrefix(name, "xl/worksheets/sheet") ||
			name == "content.xml" ||
			strings.HasSuffix(name, ".xhtml") ||
			strings.HasSuffix(name, ".html")
		if !isContent {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		dec := xml.NewDecoder(bytes.NewReader(b))
		dec.Strict = false
		var parts []string
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if cd, ok := tok.(xml.CharData); ok {
				t := strings.TrimSpace(string(cd))
				if t != "" {
					parts = append(parts, t)
				}
			}
		}
		if len(parts) > 0 {
			sections = append(sections, strings.Join(parts, " "))
		}
	}
	if len(sections) == 0 {
		return "", fmt.Errorf("no readable text in archive")
	}
	return strings.Join(sections, "\n\n"), nil
}
