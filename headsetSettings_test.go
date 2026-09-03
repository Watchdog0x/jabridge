package main

import "testing"

func TestSettingValueLabel(t *testing.T) {
	tests := []struct {
		name string
		s    settingInfo
		want string
	}{
		{
			name: "Toggle ON",
			s: settingInfo{
				cntrlType:    cntrlToggle,
				currByte:     1,
				listKeyValue: nil,
			},
			want: "ON",
		},
		{
			name: "Toggle OFF",
			s: settingInfo{
				cntrlType:    cntrlToggle,
				currByte:     0,
				listKeyValue: nil,
			},
			want: "OFF",
		},
		{
			name: "List match",
			s: settingInfo{
				currByte: 2,
				listKeyValue: []listKeyValue{
					{key: 1, value: "Low"},
					{key: 2, value: "Medium"},
					{key: 3, value: "High"},
				},
			},
			want: "Medium",
		},
		{
			name: "List no match",
			s: settingInfo{
				currByte: 99,
				listKeyValue: []listKeyValue{
					{key: 1, value: "Low"},
					{key: 2, value: "Medium"},
				},
			},
			want: "(99)",
		},
		{
			name: "String datatype",
			s: settingInfo{
				dataType:   1,
				currString: "hello",
			},
			want: "hello",
		},
		{
			name: "Byte with no list no toggle",
			s: settingInfo{
				dataType:     0,
				cntrlType:    cntrlComboBox,
				currByte:     42,
				listKeyValue: nil,
			},
			want: "42",
		},
		{
			name: "List overrides toggle",
			s: settingInfo{
				cntrlType: cntrlToggle,
				currByte:  1,
				listKeyValue: []listKeyValue{
					{key: 0, value: "Disabled"},
					{key: 1, value: "Enabled"},
				},
			},
			want: "Enabled",
		},
		{
			name: "Empty list with toggle",
			s: settingInfo{
				cntrlType:    cntrlToggle,
				currByte:     1,
				listKeyValue: []listKeyValue{},
			},
			want: "ON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := settingValueLabel(tt.s)
			if got != tt.want {
				t.Errorf("settingValueLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}
