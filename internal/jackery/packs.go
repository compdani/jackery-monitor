package jackery

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
)

func NormalizePacks(raw []map[string]any) []map[string]any {
	unique := map[string]map[string]any{}
	for _, pack := range raw {
		sn := str(pack["deviceSn"])
		if sn == "" || number(pack["isDelete"]) != 0 {
			continue
		}
		unique[sn] = pack
	}
	result := make([]map[string]any, 0, len(unique))
	for _, pack := range unique {
		result = append(result, pack)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := number(result[i]["deviceOrder"]), number(result[j]["deviceOrder"])
		if a == b {
			return str(result[i]["deviceSn"]) < str(result[j]["deviceSn"])
		}
		return a < b
	})
	return result
}
func (c *Client) Packs(ctx context.Context, sn string) ([]map[string]any, error) {
	data, err := c.get(ctx, "/v1/device/battery/pack/list", url.Values{"deviceSn": {sn}})
	if err != nil {
		return nil, err
	}
	raw, ok := data["data"].([]any)
	if !ok {
		return nil, errors.New("cloud response missing battery pack list")
	}
	packs := []map[string]any{}
	for _, v := range raw {
		if p, ok := v.(map[string]any); ok {
			packs = append(packs, p)
		}
	}
	return NormalizePacks(packs), nil
}
func (c *Client) SetPackHandler(handler func(string, []map[string]any)) { c.packHandler = handler }
func ParsePacks(raw []byte) (string, []map[string]any, bool) {
	var p struct {
		Type string `json:"messageType"`
		SN   string `json:"deviceSn"`
		Body struct {
			Packs []map[string]any `json:"subDevices"`
		} `json:"body"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Type != "SubDevicePropertyChange" || p.SN == "" || p.Body.Packs == nil {
		return "", nil, false
	}
	return p.SN, NormalizePacks(p.Body.Packs), true
}
