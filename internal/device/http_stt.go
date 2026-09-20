package device

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"sync"
)

// HTTPTranscriber implements the multipart audio-transcription protocol used by
// OpenAI and compatible local speech servers. Endpoint selection is operator
// configuration; provider responses never contain instructions to execute.
type HTTPTranscriber struct {
	URL, Model, APIKey string
	Client             *http.Client
}

func NewHTTPTranscriber(endpoint, model, key string) (*HTTPTranscriber, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(u.Hostname())
	if u.User != nil || u.Hostname() == "" || u.Fragment != "" || u.Scheme != "https" && (u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()))) {
		return nil, errors.New("STT endpoint requires HTTPS or loopback HTTP")
	}
	if model == "" {
		return nil, errors.New("STT model is required")
	}
	return &HTTPTranscriber{URL: endpoint, Model: model, APIKey: key, Client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *HTTPTranscriber) Transcribe(ctx context.Context, pcm []byte, locale string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "voice.wav")
	if err != nil {
		return "", err
	}
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(pcm)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], 16000)
	binary.LittleEndian.PutUint32(header[28:], 32000)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))
	if _, err = file.Write(header); err != nil {
		return "", err
	}
	if _, err = file.Write(pcm); err != nil {
		return "", err
	}
	for k, v := range map[string]string{"model": p.Model, "language": locale, "response_format": "json"} {
		if err = writer.WriteField(k, v); err != nil {
			return "", err
		}
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	// Transport may still read the body after Do returns on cancellation.
	// Serialize scrubbing with those reads, including early provider responses.
	upload := &audioUpload{data: body.Bytes()}
	defer upload.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", p.URL, upload)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	res, err := p.Client.Do(req)
	if err != nil {
		return "", errors.New("transcription provider unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", errors.New("transcription provider rejected request")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(data) > 8192 {
		return "", errors.New("transcription provider response exceeded limit")
	}
	defer clear(data)
	var response struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(data, &response) != nil || len(response.Text) > 1024 {
		return "", errors.New("invalid transcription response")
	}
	return response.Text, nil
}

type audioUpload struct {
	mu     sync.Mutex
	data   []byte
	offset int
	closed bool
}

func (b *audioUpload) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.offset == len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.offset:])
	b.offset += n
	return n, nil
}
func (b *audioUpload) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	clear(b.data)
	b.closed = true
	return nil
}
