package soap

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Value is the CustomTargetingValue as the SOAP service models it. It differs
// from the REST client.CustomTargetingValue: the SOAP object is keyed by numeric
// ids (customTargetingKeyId, id) rather than resource names, and its "name" field
// is the value string that REST calls adTagName.
//
// Field order mirrors the WSDL CustomTargetingValue sequence exactly
// (customTargetingKeyId, id, name, displayName, matchType, status); encoding/xml
// emits struct fields in declaration order, and the document/literal binding is
// order-sensitive. omitempty drops the read-only id on create and never sends the
// read-only status on writes.
type Value struct {
	CustomTargetingKeyID int64  `xml:"customTargetingKeyId"`
	ID                   int64  `xml:"id,omitempty"`
	Name                 string `xml:"name"`
	DisplayName          string `xml:"displayName,omitempty"`
	MatchType            string `xml:"matchType,omitempty"`
	Status               string `xml:"status,omitempty"`
}

// --- create ------------------------------------------------------------------

type createRequest struct {
	XMLName xml.Name `xml:"createCustomTargetingValues"`
	Xmlns   string   `xml:"xmlns,attr"`
	Values  []Value  `xml:"values"`
}

type createResponse struct {
	RVal []Value `xml:"Body>createCustomTargetingValuesResponse>rval"`
}

// CreateCustomTargetingValue creates a single custom targeting value and returns
// it as the service echoes it back (with the server-assigned id populated).
//
// The required fields are customTargetingKeyId and name; matchType is required by
// the resource. displayName is optional. The service create call takes a list;
// the shim sends exactly one so a caller can never accidentally create a batch.
func (c *Client) CreateCustomTargetingValue(ctx context.Context, v Value) (*Value, error) {
	req := &createRequest{Xmlns: apiNamespace, Values: []Value{v}}
	var resp createResponse
	if err := c.call(ctx, req, &resp); err != nil {
		return nil, err
	}
	if len(resp.RVal) == 0 {
		return nil, errors.New("soap: createCustomTargetingValues returned no value")
	}
	return &resp.RVal[0], nil
}

// --- update ------------------------------------------------------------------

type updateRequest struct {
	XMLName xml.Name `xml:"updateCustomTargetingValues"`
	Xmlns   string   `xml:"xmlns,attr"`
	Values  []Value  `xml:"values"`
}

type updateResponse struct {
	RVal []Value `xml:"Body>updateCustomTargetingValuesResponse>rval"`
}

// UpdateCustomTargetingValue updates a single value. SOAP update REPLACES the
// whole object, so the caller must pass a fully populated Value (id plus the
// immutable customTargetingKeyId, name, and matchType carried from the current
// state, with the mutable displayName set to the new value). Status is read-only
// and must be left unset; omitempty keeps it off the wire.
func (c *Client) UpdateCustomTargetingValue(ctx context.Context, v Value) (*Value, error) {
	if v.ID == 0 {
		return nil, errors.New("soap: UpdateCustomTargetingValue requires a non-zero value id")
	}
	req := &updateRequest{Xmlns: apiNamespace, Values: []Value{v}}
	var resp updateResponse
	if err := c.call(ctx, req, &resp); err != nil {
		return nil, err
	}
	if len(resp.RVal) == 0 {
		return nil, errors.New("soap: updateCustomTargetingValues returned no value")
	}
	return &resp.RVal[0], nil
}

// --- delete (deactivate) -----------------------------------------------------

type performActionRequest struct {
	XMLName xml.Name    `xml:"performCustomTargetingValueAction"`
	Xmlns   string      `xml:"xmlns,attr"`
	Action  valueAction `xml:"customTargetingValueAction"`
	Filter  statement   `xml:"filterStatement"`
}

// valueAction is the abstract CustomTargetingValueAction. The concrete action is
// selected by xsi:type (here DeleteCustomTargetingValues, which deactivates).
type valueAction struct {
	XsiType string `xml:"xsi:type,attr"`
}

// statement is a PQL Statement: a query plus bind-variable values. Order matches
// the WSDL (query, then values).
type statement struct {
	Query  string     `xml:"query"`
	Values []mapEntry `xml:"values"`
}

// mapEntry is a String_ValueMapEntry: a bind-variable name (key) and its typed
// value.
type mapEntry struct {
	Key   string   `xml:"key"`
	Value pqlValue `xml:"value"`
}

// pqlValue is an abstract Value; the concrete subtype is chosen by xsi:type. Both
// bind variables here are ids, so NumberValue (a numeric value carried as a
// string) is used.
type pqlValue struct {
	XsiType string `xml:"xsi:type,attr"`
	Value   string `xml:"value"`
}

type performActionResponse struct {
	NumChanges int `xml:"Body>performCustomTargetingValueActionResponse>rval>numChanges"`
}

// DeleteCustomTargetingValue deactivates exactly one value via
// performCustomTargetingValueAction with the DeleteCustomTargetingValues action.
// SOAP "delete" is a soft delete: the value's status becomes INACTIVE (values
// have no hard delete), which is the provider's documented destroy semantics.
//
// The PQL filter is scoped by BIND VARIABLES to this key/value pair
// (WHERE customTargetingKeyId = :keyId AND id = :valueId). Ids are passed as
// typed NumberValue bind values, never interpolated into the query string, so
// there is no PQL-injection surface and no way to sweep in an unrelated value.
// It returns numChanges (0 or 1).
func (c *Client) DeleteCustomTargetingValue(ctx context.Context, keyID, valueID int64) (int, error) {
	req := &performActionRequest{
		Xmlns:  apiNamespace,
		Action: valueAction{XsiType: tnsType("DeleteCustomTargetingValues")},
		Filter: statement{
			Query: "WHERE customTargetingKeyId = :keyId AND id = :valueId",
			Values: []mapEntry{
				{Key: "keyId", Value: pqlValue{XsiType: tnsType("NumberValue"), Value: strconv.FormatInt(keyID, 10)}},
				{Key: "valueId", Value: pqlValue{XsiType: tnsType("NumberValue"), Value: strconv.FormatInt(valueID, 10)}},
			},
		},
	}
	var resp performActionResponse
	if err := c.call(ctx, req, &resp); err != nil {
		return 0, err
	}
	return resp.NumChanges, nil
}

// --- ID bridging: REST resource names <-> SOAP numeric ids -------------------

// KeyIDFromResourceName extracts the numeric customTargetingKeyId from a key
// resource name (networks/{code}/customTargetingKeys/{id}). A bare numeric id is
// accepted as-is.
func KeyIDFromResourceName(name string) (int64, error) {
	return numericIDFromName(name, "customTargetingKeys")
}

// ValueIDFromResourceName extracts the numeric id from a value resource name
// (networks/{code}/customTargetingValues/{id}). A bare numeric id is accepted.
func ValueIDFromResourceName(name string) (int64, error) {
	return numericIDFromName(name, "customTargetingValues")
}

// ValueResourceName builds the flat REST resource name for a value id under the
// shim's network: networks/{networkCode}/customTargetingValues/{id}. This bridges
// a SOAP-created numeric id back to the name the REST read side expects.
func (c *Client) ValueResourceName(id int64) string {
	return fmt.Sprintf("networks/%s/customTargetingValues/%d", c.networkCode, id)
}

// numericIDFromName pulls the trailing numeric id out of a resource name,
// tolerating a bare id. collection names the expected segment purely for clearer
// error messages.
func numericIDFromName(name, collection string) (int64, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return 0, fmt.Errorf("soap: empty %s resource name", collection)
	}
	idPart := trimmed
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		idPart = trimmed[i+1:]
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("soap: %s resource name %q has no numeric id", collection, name)
	}
	return id, nil
}
