package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBundlesURL = "https://cdn.cloud.jabra.com/models/v/16/product-group-bundles/bundles.json"
	defaultModelsBase = "https://cdn.cloud.jabra.com/models/v/16"
	defaultModelName  = "Jabra SDK V4"
	defaultSchema     = "1.10.0"
	maxCatalogBytes   = 32 << 20
)

type Client struct {
	HTTPClient    *http.Client
	BundlesURL    string
	ModelsBaseURL string
	ModelName     string
	SchemaVersion string
}

type Capabilities struct {
	ProductName string
	PID         uint16
	Variant     string
	Firmware    string
	Properties  map[string]Property
}

type Property struct {
	Name            string
	Access          string
	RequiresRestart bool
	PossibleValues  []string
}

type catalogDocument struct {
	Bundles []struct {
		Products []catalogProduct `json:"products"`
	} `json:"bundles"`
	UnbundledProducts []catalogProduct `json:"unbundledProducts"`
}

type catalogProduct struct {
	ProductName string `json:"productName"`
	Variants    []struct {
		VendorID    int    `json:"vendorId"`
		ProductID   int    `json:"productId"`
		VariantType string `json:"variantType"`
	} `json:"variants"`
	FirmwareReleases []struct {
		Version string `json:"version"`
		Revoked bool   `json:"revoked"`
	} `json:"firmwareReleases"`
}

func NewClient() *Client {
	return &Client{
		HTTPClient:    &http.Client{Timeout: 8 * time.Second},
		BundlesURL:    defaultBundlesURL,
		ModelsBaseURL: defaultModelsBase,
		ModelName:     defaultModelName,
		SchemaVersion: defaultSchema,
	}
}

func (client *Client) Lookup(ctx context.Context, pid uint16, variant, firmware string) (*Capabilities, error) {
	if client == nil {
		return nil, errors.New("nil model catalog client")
	}
	var catalog catalogDocument
	if err := client.getJSON(ctx, client.BundlesURL, &catalog); err != nil {
		return nil, fmt.Errorf("load model catalog: %w", err)
	}
	products := make([]catalogProduct, 0, len(catalog.UnbundledProducts)+32)
	for _, bundle := range catalog.Bundles {
		products = append(products, bundle.Products...)
	}
	products = append(products, catalog.UnbundledProducts...)

	var matchedProduct *catalogProduct
	matchedVariant := ""
	for index := range products {
		for _, candidate := range products[index].Variants {
			if candidate.VendorID != 0x0b0e || candidate.ProductID != int(pid) {
				continue
			}
			if variant != "" && !strings.EqualFold(candidate.VariantType, variant) {
				continue
			}
			matchedProduct = &products[index]
			matchedVariant = candidate.VariantType
			break
		}
		if matchedProduct != nil {
			break
		}
	}
	if matchedProduct == nil {
		return nil, fmt.Errorf("PID 0x%04x variant %q is not in the current model catalog", pid, variant)
	}
	selectedFirmware := firmware
	if selectedFirmware == "" {
		selectedFirmware = newestFirmware(matchedProduct.FirmwareReleases)
	}
	if selectedFirmware == "" {
		return nil, fmt.Errorf("model %s has no public firmware entry", matchedProduct.ProductName)
	}

	schemaURL := strings.TrimRight(client.ModelsBaseURL, "/") +
		fmt.Sprintf("/vendors/%d/products/%d/variants/%s/firmware-versions/%s/device-models/%s/schema-versions/%s.json",
			0x0b0e,
			pid,
			url.PathEscape(matchedVariant),
			url.PathEscape(selectedFirmware),
			url.PathEscape(client.ModelName),
			url.PathEscape(client.SchemaVersion),
		)
	var schema any
	if err := client.getJSON(ctx, schemaURL, &schema); err != nil {
		return nil, fmt.Errorf("load model schema: %w", err)
	}
	properties := make(map[string]Property)
	collectProperties(schema, properties)
	return &Capabilities{
		ProductName: matchedProduct.ProductName,
		PID:         pid,
		Variant:     matchedVariant,
		Firmware:    selectedFirmware,
		Properties:  properties,
	}, nil
}

func (client *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 8 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", endpoint, response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxCatalogBytes))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func newestFirmware(releases []struct {
	Version string `json:"version"`
	Revoked bool   `json:"revoked"`
}) string {
	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		if release.Version != "" && !release.Revoked {
			versions = append(versions, release.Version)
		}
	}
	sort.SliceStable(versions, func(i, j int) bool { return compareVersions(versions[i], versions[j]) > 0 })
	if len(versions) == 0 {
		return ""
	}
	return versions[0]
}

func compareVersions(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	count := len(leftParts)
	if len(rightParts) > count {
		count = len(rightParts)
	}
	for index := 0; index < count; index++ {
		leftValue, rightValue := 0, 0
		if index < len(leftParts) {
			leftValue, _ = strconv.Atoi(leftParts[index])
		}
		if index < len(rightParts) {
			rightValue, _ = strconv.Atoi(rightParts[index])
		}
		if leftValue > rightValue {
			return 1
		}
		if leftValue < rightValue {
			return -1
		}
	}
	return 0
}

func collectProperties(value any, output map[string]Property) {
	switch typed := value.(type) {
	case []any:
		for _, entry := range typed {
			collectProperties(entry, output)
		}
	case map[string]any:
		names := propertyNames(typed)
		for _, name := range names {
			property := output[name]
			property.Name = name
			if access, ok := typed["settingAccess"].(string); ok {
				property.Access = access
			}
			if restart, ok := typed["requiresRestart"].(bool); ok {
				property.RequiresRestart = restart
			}
			if possible, ok := typed["possibleValues"].([]any); ok {
				for _, raw := range possible {
					if item, ok := raw.(map[string]any); ok {
						property.PossibleValues = append(property.PossibleValues, fmt.Sprint(item["value"]))
					}
				}
			}
			output[name] = property
		}
		for _, child := range typed {
			collectProperties(child, output)
		}
	}
}

func propertyNames(object map[string]any) []string {
	names := make([]string, 0)
	if values, ok := object["sdkProperties"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok && name != "" {
				names = append(names, name)
			}
		}
	}
	if name, ok := object["sdkProperty"].(string); ok && name != "" {
		names = append(names, name)
	}
	return names
}
