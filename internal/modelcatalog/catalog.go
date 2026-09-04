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
	ProductName              string
	ProductGroupName         string
	DeviceType               string
	PID                      uint16
	Variant                  string
	Firmware                 string
	FirmwareProtocol         int
	FirmwareProtocolKnown    bool
	FirmwareDowngradeAllowed bool
	SupportedProtocols       []string
	Properties               map[string]Property
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
	ProductName              string            `json:"productName"`
	ProductGroupName         string            `json:"productGroupName"`
	DeviceType               string            `json:"deviceType"`
	FirmwareDowngradeAllowed bool              `json:"firmwareDowngradeAllowed"`
	SupportedProtocols       []string          `json:"supportedProtocols"`
	Variants                 []catalogVariant  `json:"variants"`
	FirmwareReleases         []firmwareRelease `json:"firmwareReleases"`
}

type catalogVariant struct {
	VendorID         int    `json:"vendorId"`
	ProductID        int    `json:"productId"`
	VariantType      string `json:"variantType"`
	Name             string `json:"name"`
	FirmwareProtocol *int   `json:"fwuProtocolId"`
}

type firmwareRelease struct {
	Version     string `json:"version"`
	MD5Checksum string `json:"md5Checksum"`
	Revoked     bool   `json:"revoked"`
}

// Inventory is the current public model catalog. A catalog entry means that
// Jabra publishes metadata for the device; it is not a Jabridge hardware-test
// result.
type Inventory struct {
	Products                       []Product
	AllProductProfiles             int
	JabraProductProfiles           int
	PartnerProductProfiles         int
	ProductGroups                  int
	Variants                       int
	USBProductIDs                  int
	FirmwareProtocols              []int
	HasUnspecifiedFirmwareProtocol bool
}

type Product struct {
	ProductName              string
	ProductGroupName         string
	DeviceType               string
	FirmwareDowngradeAllowed bool
	SupportedProtocols       []string
	Variants                 []Variant
}

type Variant struct {
	Name                  string
	PID                   uint16
	VariantType           string
	FirmwareProtocol      int
	FirmwareProtocolKnown bool
}

// ReleaseEvidence describes one firmware version using the checksum and PID
// relationships published in Jabra's model catalog.
type ReleaseEvidence struct {
	Version                        string
	MD5Checksum                    string
	CompatiblePIDs                 []uint16
	FirmwareProtocols              []int
	HasUnspecifiedFirmwareProtocol bool
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

func (client *Client) loadProducts(ctx context.Context) ([]catalogProduct, int, error) {
	if client == nil {
		return nil, 0, errors.New("nil model catalog client")
	}
	var catalog catalogDocument
	if err := client.getJSON(ctx, client.BundlesURL, &catalog); err != nil {
		return nil, 0, fmt.Errorf("load model catalog: %w", err)
	}
	products := make([]catalogProduct, 0, len(catalog.UnbundledProducts)+32)
	for _, bundle := range catalog.Bundles {
		products = append(products, bundle.Products...)
	}
	products = append(products, catalog.UnbundledProducts...)
	return products, len(products), nil
}

// List returns every Jabra USB model in the current public catalog. Partner
// products using another USB vendor ID are counted but excluded from Products.
func (client *Client) List(ctx context.Context) (*Inventory, error) {
	products, allProfiles, err := client.loadProducts(ctx)
	if err != nil {
		return nil, err
	}

	inventory := &Inventory{AllProductProfiles: allProfiles}
	groups := make(map[string]struct{})
	pids := make(map[uint16]struct{})
	protocols := make(map[int]struct{})
	for _, source := range products {
		product := Product{
			ProductName:              source.ProductName,
			ProductGroupName:         source.ProductGroupName,
			DeviceType:               source.DeviceType,
			FirmwareDowngradeAllowed: source.FirmwareDowngradeAllowed,
			SupportedProtocols:       append([]string(nil), source.SupportedProtocols...),
		}
		for _, sourceVariant := range source.Variants {
			if sourceVariant.VendorID != 0x0b0e || sourceVariant.ProductID < 0 || sourceVariant.ProductID > 0xffff {
				continue
			}
			variant := Variant{
				Name:        sourceVariant.Name,
				PID:         uint16(sourceVariant.ProductID),
				VariantType: sourceVariant.VariantType,
			}
			if sourceVariant.FirmwareProtocol != nil {
				variant.FirmwareProtocol = *sourceVariant.FirmwareProtocol
				variant.FirmwareProtocolKnown = true
				protocols[variant.FirmwareProtocol] = struct{}{}
			} else {
				inventory.HasUnspecifiedFirmwareProtocol = true
			}
			product.Variants = append(product.Variants, variant)
			pids[variant.PID] = struct{}{}
			inventory.Variants++
		}
		if len(product.Variants) == 0 {
			inventory.PartnerProductProfiles++
			continue
		}
		inventory.JabraProductProfiles++
		group := product.ProductGroupName
		if group == "" {
			group = product.ProductName
		}
		groups[group] = struct{}{}
		inventory.Products = append(inventory.Products, product)
	}
	inventory.ProductGroups = len(groups)
	inventory.USBProductIDs = len(pids)
	for protocol := range protocols {
		inventory.FirmwareProtocols = append(inventory.FirmwareProtocols, protocol)
	}
	sort.Ints(inventory.FirmwareProtocols)
	sort.SliceStable(inventory.Products, func(i, j int) bool {
		return inventory.Products[i].ProductName < inventory.Products[j].ProductName
	})
	return inventory, nil
}

// FirmwareRelease returns the published checksum and every Jabra USB PID that
// is tied to the exact same release bytes. This handles UC/MS sibling IDs whose
// archive manifest contains only one canonical target PID.
func (client *Client) FirmwareRelease(ctx context.Context, pid uint16, version string) (*ReleaseEvidence, error) {
	products, _, err := client.loadProducts(ctx)
	if err != nil {
		return nil, err
	}
	checksums := make(map[string]struct{})
	protocols := make(map[int]struct{})
	unspecifiedProtocol := false
	for _, product := range products {
		matchesPID := false
		for _, variant := range product.Variants {
			if variant.VendorID != 0x0b0e || variant.ProductID != int(pid) {
				continue
			}
			matchesPID = true
			if variant.FirmwareProtocol == nil {
				unspecifiedProtocol = true
			} else {
				protocols[*variant.FirmwareProtocol] = struct{}{}
			}
		}
		if !matchesPID {
			continue
		}
		for _, release := range product.FirmwareReleases {
			if release.Version == version && !release.Revoked && release.MD5Checksum != "" {
				checksums[release.MD5Checksum] = struct{}{}
			}
		}
	}
	if len(checksums) == 0 {
		return nil, fmt.Errorf("PID 0x%04x firmware %s has no current public checksum", pid, version)
	}
	if len(checksums) != 1 {
		return nil, fmt.Errorf("PID 0x%04x firmware %s has conflicting public checksums", pid, version)
	}
	checksum := ""
	for value := range checksums {
		checksum = value
	}

	compatible := make(map[uint16]struct{})
	for _, product := range products {
		matchesRelease := false
		for _, release := range product.FirmwareReleases {
			if release.Version == version && !release.Revoked && release.MD5Checksum == checksum {
				matchesRelease = true
				break
			}
		}
		if !matchesRelease {
			continue
		}
		for _, variant := range product.Variants {
			if variant.VendorID == 0x0b0e && variant.ProductID >= 0 && variant.ProductID <= 0xffff {
				compatible[uint16(variant.ProductID)] = struct{}{}
			}
		}
	}

	evidence := &ReleaseEvidence{
		Version:                        version,
		MD5Checksum:                    checksum,
		HasUnspecifiedFirmwareProtocol: unspecifiedProtocol,
	}
	for compatiblePID := range compatible {
		evidence.CompatiblePIDs = append(evidence.CompatiblePIDs, compatiblePID)
	}
	for protocol := range protocols {
		evidence.FirmwareProtocols = append(evidence.FirmwareProtocols, protocol)
	}
	sort.SliceStable(evidence.CompatiblePIDs, func(i, j int) bool {
		return evidence.CompatiblePIDs[i] < evidence.CompatiblePIDs[j]
	})
	sort.Ints(evidence.FirmwareProtocols)
	return evidence, nil
}

func (client *Client) Lookup(ctx context.Context, pid uint16, variant, firmware string) (*Capabilities, error) {
	products, _, err := client.loadProducts(ctx)
	if err != nil {
		return nil, err
	}

	var matchedProduct *catalogProduct
	matchedVariant := ""
	matchedFirmwareProtocol := 0
	matchedFirmwareProtocolKnown := false
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
			if candidate.FirmwareProtocol != nil {
				matchedFirmwareProtocol = *candidate.FirmwareProtocol
				matchedFirmwareProtocolKnown = true
			}
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
		ProductName:              matchedProduct.ProductName,
		ProductGroupName:         matchedProduct.ProductGroupName,
		DeviceType:               matchedProduct.DeviceType,
		PID:                      pid,
		Variant:                  matchedVariant,
		Firmware:                 selectedFirmware,
		FirmwareProtocol:         matchedFirmwareProtocol,
		FirmwareProtocolKnown:    matchedFirmwareProtocolKnown,
		FirmwareDowngradeAllowed: matchedProduct.FirmwareDowngradeAllowed,
		SupportedProtocols:       append([]string(nil), matchedProduct.SupportedProtocols...),
		Properties:               properties,
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

func newestFirmware(releases []firmwareRelease) string {
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
