// Minimal S3 client: a single SigV4-signed PUT object, net/http only (no AWS
// SDK dependency). Path-style addressing, which is what MinIO expects
// (http://minio:9000/{bucket}/{key}). Used by the GDPR export workflow to
// drop JSON bundles into the `exports` bucket.
package activities

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// s3Put uploads body to {endpoint}/{bucket}/{key} with AWS Signature V4.
// Keys must use SigV4-safe characters ([A-Za-z0-9/._-]) — the canonical URI
// is sent unescaped, which is valid for that set.
func s3Put(ctx context.Context, hc *http.Client, endpoint, region, bucket, key string, body []byte, accessKey, secretKey string) error {
	if accessKey == "" || secretKey == "" {
		return fmt.Errorf("s3 credentials are empty (S3_ACCESS_KEY / S3_SECRET_KEY)")
	}
	if err := validateS3Key(key); err != nil {
		return err
	}
	endpoint = strings.TrimRight(endpoint, "/")
	url := endpoint + "/" + bucket + "/" + key
	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	const signedHeaders = "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{
		http.MethodPut,
		"/" + bucket + "/" + key,
		"",
		"host:" + host + "\n" +
			"x-amz-content-sha256:" + payloadHash + "\n" +
			"x-amz-date:" + amzDate + "\n",
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStamp + "/" + region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))
	signature := hex.EncodeToString(hmacSHA256(sigV4Key(secretKey, dateStamp, region), []byte(stringToSign)))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put %s/%s: %w", bucket, key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("s3 put %s/%s: status %d: %s", bucket, key, resp.StatusCode, string(b))
	}
	return nil
}

func validateS3Key(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") {
		return fmt.Errorf("invalid s3 key %q", key)
	}
	for _, r := range key {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '/' || r == '.' || r == '_' || r == '-'
		if !ok {
			return fmt.Errorf("s3 key %q contains unsupported character %q", key, r)
		}
	}
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// sigV4Key derives the AWS Signature V4 signing key for service s3.
func sigV4Key(secret, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}
