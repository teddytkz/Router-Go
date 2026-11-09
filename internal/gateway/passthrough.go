package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// multipartModel reads the "model" field of a multipart/form-data body.
func multipartModel(raw []byte, ctype string) (string, error) {
	mr, err := multipartReader(raw, ctype)
	if err != nil {
		return "", err
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return "", nil
		} else if err != nil {
			return "", err
		}
		if part.FormName() == "model" {
			b, _ := io.ReadAll(io.LimitReader(part, 1<<10))
			return strings.TrimSpace(string(b)), nil
		}
	}
}

func multipartReader(raw []byte, ctype string) (*multipart.Reader, error) {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil || !strings.HasPrefix(mt, "multipart/") || params["boundary"] == "" {
		return nil, errors.New("missing multipart boundary")
	}
	return multipart.NewReader(bytes.NewReader(raw), params["boundary"]), nil
}

// passBody builds the upstream body of a passthrough kind: the client's own body with "model" set to the upstream's
// name. A JSON body keeps its field order-insensitive content; a multipart body is re-written part by part (files are
// copied untouched) and the new content type, with its boundary, is returned.
func passBody(req map[string]any, raw []byte, ctype, model string) ([]byte, string, error) {
	if !strings.HasPrefix(ctype, "multipart/") {
		body := make(map[string]any, len(req))
		for k, v := range req {
			body[k] = v
		}
		body["model"] = model
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		err := enc.Encode(body)
		return buf.Bytes(), "", err
	}
	mr, err := multipartReader(raw, ctype)
	if err != nil {
		return nil, "", err
	}
	var out bytes.Buffer
	mw := multipart.NewWriter(&out)
	sawModel := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, "", err
		}
		w, err := mw.CreatePart(part.Header)
		if err != nil {
			return nil, "", err
		}
		if part.FormName() == "model" {
			sawModel = true
			_, err = io.WriteString(w, model)
		} else {
			_, err = io.Copy(w, part)
		}
		if err != nil {
			return nil, "", err
		}
	}
	if !sawModel {
		_ = mw.WriteField("model", model)
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return out.Bytes(), mw.FormDataContentType(), nil
}
