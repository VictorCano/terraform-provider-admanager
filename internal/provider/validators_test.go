package provider

// P1-2 (L3 half): plan-time schema validators, exercised credential-free by
// invoking each validator directly. These need no API round-trip, so they belong
// in L3 rather than the acceptance suite. Covered: the match_type OneOf enum, the
// custom_targeting_key resource-name RegexMatches, and the ad_tag_name length
// caps on both the value (40) and key (10), plus the key type / reportable_type
// enums.

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// runStringValidators applies every validator on the named string attribute to
// value v and returns the accumulated diagnostics.
func runStringValidators(t *testing.T, sch rschema.Schema, name, v string) diag.Diagnostics {
	t.Helper()
	a, ok := sch.Attributes[name].(rschema.StringAttribute)
	if !ok {
		t.Fatalf("%s is not a StringAttribute", name)
	}
	var resp validator.StringResponse
	req := validator.StringRequest{Path: path.Root(name), ConfigValue: types.StringValue(v)}
	for _, val := range a.Validators {
		val.ValidateString(context.Background(), req, &resp)
	}
	return resp.Diagnostics
}

// firstSummary returns the summary of the first error diagnostic, or "".
func firstSummary(diags diag.Diagnostics) string {
	if errs := diags.Errors(); len(errs) > 0 {
		return errs[0].Summary()
	}
	return ""
}

func TestCustomTargetingValueMatchTypeValidator(t *testing.T) {
	sch := valueTestSchema(t)
	for _, ok := range customTargetingValueMatchTypes {
		if diags := runStringValidators(t, sch, "match_type", ok); diags.HasError() {
			t.Errorf("match_type %q should be accepted: %v", ok, diags)
		}
	}
	// An explicit empty string is a non-member (distinct from a null config value,
	// which OneOf skips) and must be rejected like any other invalid value.
	for _, bad := range []string{"FUZZY", "exact", "", "UNSPECIFIED"} {
		diags := runStringValidators(t, sch, "match_type", bad)
		if !diags.HasError() {
			t.Errorf("match_type %q should be rejected", bad)
			continue
		}
		if got := firstSummary(diags); got != "Invalid Attribute Value Match" {
			t.Errorf("match_type %q summary = %q, want %q", bad, got, "Invalid Attribute Value Match")
		}
	}
}

func TestCustomTargetingValueKeyReferenceValidator(t *testing.T) {
	sch := valueTestSchema(t)
	for _, ok := range []string{
		"networks/123456/customTargetingKeys/321",
		"networks/1/customTargetingKeys/9",
	} {
		if diags := runStringValidators(t, sch, "custom_targeting_key", ok); diags.HasError() {
			t.Errorf("custom_targeting_key %q should be accepted: %v", ok, diags)
		}
	}
	for _, bad := range []string{
		"321",                     // bare numeric id
		"customTargetingKeys/321", // partial
		"networks/123456/customTargetingKeys/abc",   // non-numeric id
		"networks/123456/customTargetingValues/321", // wrong collection
	} {
		diags := runStringValidators(t, sch, "custom_targeting_key", bad)
		if !diags.HasError() {
			t.Errorf("custom_targeting_key %q should be rejected", bad)
			continue
		}
		if got := firstSummary(diags); got != "Invalid Attribute Value Match" {
			t.Errorf("custom_targeting_key %q summary = %q, want %q", bad, got, "Invalid Attribute Value Match")
		}
	}
}

func TestCustomTargetingValueAdTagNameLengthValidator(t *testing.T) {
	sch := valueTestSchema(t)
	if diags := runStringValidators(t, sch, "ad_tag_name", strings.Repeat("a", adTagNameValueMaxLength)); diags.HasError() {
		t.Errorf("a %d-char ad_tag_name should be accepted: %v", adTagNameValueMaxLength, diags)
	}
	diags := runStringValidators(t, sch, "ad_tag_name", strings.Repeat("a", adTagNameValueMaxLength+1))
	if !diags.HasError() {
		t.Errorf("a %d-char ad_tag_name should be rejected (max %d)", adTagNameValueMaxLength+1, adTagNameValueMaxLength)
	}
	if got := firstSummary(diags); got != "Invalid Attribute Value Length" {
		t.Errorf("summary = %q, want %q", got, "Invalid Attribute Value Length")
	}
}

func TestCustomTargetingKeyAdTagNameLengthValidator(t *testing.T) {
	sch := customTargetingKeyTestSchema(t)
	if diags := runStringValidators(t, sch, "ad_tag_name", strings.Repeat("a", adTagNameMaxLength)); diags.HasError() {
		t.Errorf("a %d-char ad_tag_name should be accepted: %v", adTagNameMaxLength, diags)
	}
	diags := runStringValidators(t, sch, "ad_tag_name", strings.Repeat("a", adTagNameMaxLength+1))
	if !diags.HasError() {
		t.Errorf("an %d-char ad_tag_name should be rejected (max %d)", adTagNameMaxLength+1, adTagNameMaxLength)
	}
	if got := firstSummary(diags); got != "Invalid Attribute Value Length" {
		t.Errorf("summary = %q, want %q", got, "Invalid Attribute Value Length")
	}
}

// forbiddenAdTagNameChars is the character denylist the schema documentation
// promises for ad_tag_name on both resources (shared by key and value).
var forbiddenAdTagNameChars = []string{
	`"`, `'`, `=`, `!`, `+`, `#`, `*`, `~`, `;`, `^`, `(`, `)`, `<`, `>`, `[`, `]`,
}

func TestCustomTargetingValueAdTagNameDenylistValidator(t *testing.T) {
	sch := valueTestSchema(t)
	// A clean value (spaces allowed for values) passes.
	for _, ok := range []string{"honda", "New York", "v-1.2"} {
		if diags := runStringValidators(t, sch, "ad_tag_name", ok); diags.HasError() {
			t.Errorf("ad_tag_name %q should be accepted: %v", ok, diags)
		}
	}
	for _, c := range forbiddenAdTagNameChars {
		v := "ab" + c + "cd"
		diags := runStringValidators(t, sch, "ad_tag_name", v)
		if !diags.HasError() {
			t.Errorf("ad_tag_name %q (contains %q) should be rejected", v, c)
			continue
		}
		if got := firstSummary(diags); got != "Invalid Attribute Value Match" {
			t.Errorf("ad_tag_name %q summary = %q, want %q", v, got, "Invalid Attribute Value Match")
		}
	}
}

func TestCustomTargetingKeyAdTagNameDenylistValidator(t *testing.T) {
	sch := customTargetingKeyTestSchema(t)
	if diags := runStringValidators(t, sch, "ad_tag_name", "color"); diags.HasError() {
		t.Errorf("ad_tag_name %q should be accepted: %v", "color", diags)
	}
	// The key denylist also forbids whitespace (values allow it, keys do not).
	for _, c := range append(append([]string{}, forbiddenAdTagNameChars...), " ", "\t") {
		v := "ab" + c + "cd"
		diags := runStringValidators(t, sch, "ad_tag_name", v)
		if !diags.HasError() {
			t.Errorf("ad_tag_name %q (contains %q) should be rejected", v, c)
			continue
		}
		if got := firstSummary(diags); got != "Invalid Attribute Value Match" {
			t.Errorf("ad_tag_name %q summary = %q, want %q", v, got, "Invalid Attribute Value Match")
		}
	}
}

func TestCustomTargetingKeyEnumValidators(t *testing.T) {
	sch := customTargetingKeyTestSchema(t)

	t.Run("type", func(t *testing.T) {
		for _, ok := range customTargetingKeyTypeValues {
			if diags := runStringValidators(t, sch, "type", ok); diags.HasError() {
				t.Errorf("type %q should be accepted: %v", ok, diags)
			}
		}
		diags := runStringValidators(t, sch, "type", "SOMETHING_ELSE")
		if got := firstSummary(diags); got != "Invalid Attribute Value Match" {
			t.Errorf("type reject summary = %q, want %q", got, "Invalid Attribute Value Match")
		}
	})

	t.Run("reportable_type", func(t *testing.T) {
		for _, ok := range customTargetingKeyReportableValues {
			if diags := runStringValidators(t, sch, "reportable_type", ok); diags.HasError() {
				t.Errorf("reportable_type %q should be accepted: %v", ok, diags)
			}
		}
		diags := runStringValidators(t, sch, "reportable_type", "MAYBE")
		if got := firstSummary(diags); got != "Invalid Attribute Value Match" {
			t.Errorf("reportable_type reject summary = %q, want %q", got, "Invalid Attribute Value Match")
		}
	})
}
