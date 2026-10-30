package comparison

import "testing"

// --- #322: a populated CR object against an EMPTY remote object ---
//
// The guard lives at the ENTRY of CompareExisting, so these cases cover all three call sites: the external
// entry from the observe comparison, the nested-map branch, and the map-element-inside-a-slice branch.
//
// The last of those is the worst in practice. An object inside an array that the remote returns empty is
// not something anyone would think to look at, and nothing in the resulting symptom points at an array.

func TestEmptyRemoteObjectIsADifference(t *testing.T) {
	cases := []struct {
		name      string
		mg        map[string]interface{}
		rm        map[string]interface{}
		wantEqual bool
		// wantPath, when set, is the field path the reason must carry. The whole value of the report is
		// that it names WHICH object was empty.
		wantPath string
	}{
		{
			// The measured case: Ironic answers "instance_info": {} for a node enrolled out of band.
			name: "nested object populated in CR, empty on remote",
			mg: map[string]interface{}{
				"instance_info": map[string]interface{}{
					"image_source":   "http://example/img",
					"image_checksum": "abc123",
				},
			},
			rm:        map[string]interface{}{"instance_info": map[string]interface{}{}},
			wantEqual: false,
			wantPath:  "instance_info",
		},
		{
			// The helpers.go entry point: a vendor returning {} for the whole resource.
			name:      "entire remote response is empty",
			mg:        map[string]interface{}{"name": "blade01", "driver": "redfish"},
			rm:        map[string]interface{}{},
			wantEqual: false,
		},
		{
			// The slice-element site. Shaped like a chart's spec.ports.
			name: "object inside a slice, empty on the remote element",
			mg: map[string]interface{}{
				"ports": []interface{}{
					map[string]interface{}{"address": "aa:bb:cc:dd:ee:ff"},
				},
			},
			rm: map[string]interface{}{
				"ports": []interface{}{
					map[string]interface{}{},
				},
			},
			wantEqual: false,
		},
		{
			name:      "empty on both sides is equal",
			mg:        map[string]interface{}{"instance_info": map[string]interface{}{}},
			rm:        map[string]interface{}{"instance_info": map[string]interface{}{}},
			wantEqual: true,
		},
		{
			// ONE-DIRECTIONAL on purpose. A map key may legitimately be absent or unset upstream, so an
			// empty CR object is not the assertion that an empty CR list is -- it is "no opinion".
			name:      "empty in CR, populated on remote, stays equal",
			mg:        map[string]interface{}{"instance_info": map[string]interface{}{}},
			rm:        map[string]interface{}{"instance_info": map[string]interface{}{"image_source": "x"}},
			wantEqual: true,
		},
		{
			// The absent-key rule must survive, because #309's remedy is built on it: a responseTransform
			// that DELETES a masked field makes the key ABSENT, not empty. If this ever starts reporting a
			// difference, every responseTransform using del() begins drifting forever.
			name:      "a key ABSENT from the remote is still skipped, not reported",
			mg:        map[string]interface{}{"driver_info": map[string]interface{}{"redfish_password": "s3cret"}},
			rm:        map[string]interface{}{"other": "value"},
			wantEqual: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := CompareExisting(tc.mg, tc.rm)
			if err != nil {
				t.Fatalf("CompareExisting() error = %v", err)
			}
			if res.IsEqual != tc.wantEqual {
				t.Fatalf("IsEqual = %v, want %v (reason: %+v)", res.IsEqual, tc.wantEqual, res.Reason)
			}
			if tc.wantEqual {
				return
			}
			if res.Reason == nil {
				t.Fatal("a difference must carry a Reason: a bare false tells an operator nothing")
			}
			if tc.wantPath != "" {
				found := false
				for _, p := range res.Reason.Path {
					if p == tc.wantPath {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Reason.Path = %v, want it to contain %q", res.Reason.Path, tc.wantPath)
				}
			}
		})
	}
}
