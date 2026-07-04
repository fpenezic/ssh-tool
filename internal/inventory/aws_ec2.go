// AWS EC2 dynamic inventory provider.
//
// Config:
//
//	{
//	  "api_token_credential_id": "<credential uuid>",  // token_id = access key, secret = secret access key
//	  "region": "eu-central-1",                         // required
//	  "hostname_source": "name_tag" | "public_ipv4" | "private_ipv4" | "public_dns"
//	}
//
// Reuses the api_token credential shape: the credential's token_id
// holds the access key, the secret holds the secret access key. One
// region per dynamic folder; for multi-region create one folder each.
// Signing is implemented inline (AWS SigV4) to avoid pulling the
// monolithic aws-sdk-go-v2 dependency for a single EC2 endpoint.

package inventory

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type AWSEC2 struct{}

func (AWSEC2) Name() string { return "aws_ec2" }

func (AWSEC2) Fetch(ctx context.Context, cfg map[string]any) ([]Entry, error) {
	accessKey, _ := cfg["api_token_id"].(string)
	secretKey, _ := cfg["api_token_secret"].(string)
	region, _ := cfg["region"].(string)
	hostSource, _ := cfg["hostname_source"].(string)
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("aws_ec2: api_token credential (access key + secret) required")
	}
	if region == "" {
		return nil, fmt.Errorf("aws_ec2: region required (e.g. eu-central-1)")
	}
	if hostSource == "" {
		hostSource = "name_tag"
	}

	host := "ec2." + region + ".amazonaws.com"
	out := []Entry{}
	nextToken := ""
	client, err := httpClient(cfg, 30*time.Second, false)
	if err != nil {
		return nil, err
	}

	for {
		q := url.Values{}
		q.Set("Action", "DescribeInstances")
		q.Set("Version", "2016-11-15")
		q.Set("MaxResults", "100")
		if nextToken != "" {
			q.Set("NextToken", nextToken)
		}
		body := q.Encode()

		req, err := http.NewRequestWithContext(ctx, "POST", "https://"+host+"/", strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		req.Header.Set("Host", host)
		if err := signAWS(req, []byte(body), accessKey, secretKey, region, "ec2"); err != nil {
			return nil, fmt.Errorf("aws_ec2: sign: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("aws_ec2: %d: %s", resp.StatusCode, truncate(string(respBody), 300))
		}

		var parsed ec2DescribeResponse
		if err := xml.Unmarshal(respBody, &parsed); err != nil {
			return nil, fmt.Errorf("aws_ec2: parse: %w", err)
		}
		for _, r := range parsed.ReservationSet {
			for _, inst := range r.InstancesSet {
				raw, _ := json.Marshal(inst)
				out = append(out, Entry{
					ExternalID: "ec2:" + inst.InstanceID,
					Name:       ec2DisplayName(inst),
					Hostname:   pickEC2Hostname(hostSource, inst),
					Kind:       KindGuestVM,
					Status:     mapEC2Status(inst.InstanceState.Name),
					Tags:       ec2Tags(inst.Tags, region),
					Raw:        raw,
				})
			}
		}

		if parsed.NextToken == "" {
			break
		}
		nextToken = parsed.NextToken
	}
	return out, nil
}

type ec2DescribeResponse struct {
	XMLName        xml.Name `xml:"DescribeInstancesResponse"`
	NextToken      string   `xml:"nextToken"`
	ReservationSet []struct {
		InstancesSet []ec2Instance `xml:"instancesSet>item"`
	} `xml:"reservationSet>item"`
}

type ec2Instance struct {
	InstanceID    string `xml:"instanceId"`
	PrivateIP     string `xml:"privateIpAddress"`
	PublicIP      string `xml:"ipAddress"`
	PrivateDNS    string `xml:"privateDnsName"`
	PublicDNS     string `xml:"dnsName"`
	InstanceState struct {
		Name string `xml:"name"`
	} `xml:"instanceState"`
	Tags []struct {
		Key   string `xml:"key"`
		Value string `xml:"value"`
	} `xml:"tagSet>item"`
}

func ec2DisplayName(i ec2Instance) string {
	for _, t := range i.Tags {
		if t.Key == "Name" && t.Value != "" {
			return t.Value
		}
	}
	return i.InstanceID
}

func pickEC2Hostname(source string, i ec2Instance) string {
	switch source {
	case "public_ipv4":
		if i.PublicIP != "" {
			return i.PublicIP
		}
		return ec2DisplayName(i)
	case "private_ipv4":
		if i.PrivateIP != "" {
			return i.PrivateIP
		}
		return ec2DisplayName(i)
	case "public_dns":
		if i.PublicDNS != "" {
			return i.PublicDNS
		}
		return ec2DisplayName(i)
	default:
		return ec2DisplayName(i)
	}
}

func mapEC2Status(s string) string {
	switch s {
	case "running":
		return "running"
	case "stopped", "stopping", "terminated", "shutting-down":
		return "stopped"
	default:
		return s
	}
}

func ec2Tags(tags []struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}, region string) []string {
	out := []string{"region=" + region}
	for _, t := range tags {
		if t.Value == "" {
			out = append(out, t.Key)
		} else {
			out = append(out, t.Key+"="+t.Value)
		}
	}
	// Stable order: AWS doesn't guarantee tag ordering between calls,
	// and the dynamic-entry change detector treats a reordered tag
	// list as a change (which, with sync on, auto-pushes every
	// refresh). Sort everything after the region prefix.
	sort.Strings(out[1:])
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ---------- SigV4 ----------

func signAWS(req *http.Request, body []byte, accessKey, secretKey, region, service string) error {
	t := time.Now().UTC()
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)

	payloadHash := sha256Hex(body)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	canonicalHeaders, signedHeaders := canonicalHeadersOf(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.Path),
		canonicalQuery(req.URL.RawQuery),
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

func canonicalHeadersOf(req *http.Request) (string, string) {
	type kv struct{ k, v string }
	var rows []kv
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		rows = append(rows, kv{lk, strings.TrimSpace(strings.Join(vs, ","))})
	}
	// Host is implicit on net/http; SigV4 needs it canonicalised.
	rows = append(rows, kv{"host", req.URL.Host})
	sort.Slice(rows, func(i, j int) bool { return rows[i].k < rows[j].k })

	var canon strings.Builder
	var signed []string
	for _, r := range rows {
		canon.WriteString(r.k)
		canon.WriteByte(':')
		canon.WriteString(r.v)
		canon.WriteByte('\n')
		signed = append(signed, r.k)
	}
	return canon.String(), strings.Join(signed, ";")
}

func canonicalURI(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return raw
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range vals[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}
