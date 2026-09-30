package perfsample

import (
	"encoding/json"
	"fmt"
)

const ReportFormat = "perfsample-report"
const CurrentReportVersion = 2

// Report 是版本化的比较报告。版本 1 只有路径和权重；版本 2 增加
// 帧身份、匹配状态，以及样本变化/符号变化的区分。
type Report struct {
	Format     string      `json:"format"`
	Version    int         `json:"version"`
	Comparison *Comparison `json:"comparison"`
}

// MarshalReport 输出当前版本报告。
func MarshalReport(cmp *Comparison) ([]byte, error) {
	if cmp == nil {
		cmp = &Comparison{}
	}
	return json.Marshal(Report{
		Format:     ReportFormat,
		Version:    CurrentReportVersion,
		Comparison: cmp,
	})
}

// UnmarshalReport 读取当前报告，也兼容无版本头的旧报告。
func UnmarshalReport(data []byte) (*Report, error) {
	var envelope struct {
		Format     string       `json:"format"`
		Version    int          `json:"version"`
		Comparison *Comparison  `json:"comparison"`
		Changes    []PathChange `json:"changes"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Comparison != nil || envelope.Changes != nil || envelope.Format != "" || envelope.Version != 0 {
		cmp := envelope.Comparison
		if cmp == nil && envelope.Changes != nil {
			cmp = &Comparison{Changes: envelope.Changes}
		}
		if cmp == nil {
			cmp = &Comparison{}
		}
		normalizeLegacyChanges(cmp)
		version := envelope.Version
		if version == 0 {
			version = 1
		}
		return &Report{Format: envelope.Format, Version: version, Comparison: cmp}, nil
	}

	// 兼容更早的裸 PathChange 数组。
	var changes []PathChange
	if err := json.Unmarshal(data, &changes); err == nil {
		cmp := &Comparison{Changes: changes}
		normalizeLegacyChanges(cmp)
		return &Report{Version: 1, Comparison: cmp}, nil
	}
	return nil, fmt.Errorf("unrecognized perfsample report")
}

func normalizeLegacyChanges(cmp *Comparison) {
	for i := range cmp.Changes {
		c := &cmp.Changes[i]
		if c.InOld && c.InNew {
			c.Status = Matched
		} else {
			c.Status = Unmatched
		}
		if c.OldIDPath == nil && c.InOld {
			c.OldIDPath = idPathFromLegacy(c.Path)
		}
		if c.NewIDPath == nil && c.InNew {
			c.NewIDPath = idPathFromLegacy(c.Path)
		}
		if !c.SampleChanged && !c.SymbolChanged {
			c.SampleChanged = absFloat(c.DeltaSelf) > 1e-9 || absFloat(c.DeltaTotal) > 1e-9
		}
	}
}

func idPathFromLegacy(path []string) []FrameID {
	ids := make([]FrameID, len(path))
	for i, name := range path {
		ids[i] = FrameID{Symbol: name}
	}
	return ids
}
