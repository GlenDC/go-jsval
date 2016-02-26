package jsval

import (
	"errors"
	"reflect"
	"regexp"

	"github.com/lestrrat/go-pdebug"
)

// Object creates a new ObjectConstraint
func Object() *ObjectConstraint {
	return &ObjectConstraint{
		additionalProperties: nil,
		minProperties:        -1,
		maxProperties:        -1,
		patternProperties:    make(map[*regexp.Regexp]Constraint),
		properties:           make(map[string]Constraint),
		propdeps:             make(map[string][]string),
		required:             make(map[string]struct{}),
		schemadeps:           make(map[string]Constraint),
	}
}

// Required specifies required property names
func (o *ObjectConstraint) Required(l ...string) *ObjectConstraint {
	o.reqlock.Lock()
	defer o.reqlock.Unlock()

	for _, pname := range l {
		o.required[pname] = struct{}{}
	}
	return o
}

// IsPropRequired returns true if the given name is listed under
// the required properties
func (o *ObjectConstraint) IsPropRequired(s string) bool {
	o.reqlock.Lock()
	defer o.reqlock.Unlock()

	_, ok := o.required[s]
	return ok
}

// MinProperties specifies the minimum number of properties this
// constraint can allow. If unspecified, it is not checked.
func (o *ObjectConstraint) MinProperties(n int) *ObjectConstraint {
	o.minProperties = n
	return o
}

// MaxProperties specifies the maximum number of properties this
// constraint can allow. If unspecified, it is not checked.
func (o *ObjectConstraint) MaxProperties(n int) *ObjectConstraint {
	o.maxProperties = n
	return o
}

// AdditionalProperties specifies the constraint that additional
// properties should be validated against.
func (o *ObjectConstraint) AdditionalProperties(c Constraint) *ObjectConstraint {
	o.additionalProperties = c
	return o
}

// AddProp adds constraints for a named property.
func (o *ObjectConstraint) AddProp(name string, c Constraint) *ObjectConstraint {
	o.proplock.Lock()
	defer o.proplock.Unlock()

	o.properties[name] = c
	return o
}

// PatternProperties specifies constraints that properties matching
// this pattern must be validated against. Note that properties listed
// using `AddProp` takes precedence.
func (o *ObjectConstraint) PatternProperties(key *regexp.Regexp, c Constraint) *ObjectConstraint {
	o.proplock.Lock()
	defer o.proplock.Unlock()

	o.patternProperties[key] = c
	return o
}

// PropDependency specifies properties that must be present when
// `from` is present.
func (o *ObjectConstraint) PropDependency(from string, to ...string) *ObjectConstraint {
	o.deplock.Lock()
	defer o.deplock.Unlock()

	l := o.propdeps[from]
	l = append(l, to...)
	o.propdeps[from] = l
	return o
}

// SchemaDependency specifies a schema that the value being validated
// must also satisfy. Note that the "object" is the target that needs to
// be additionally validated, not the value of the `from` property
func (o *ObjectConstraint) SchemaDependency(from string, c Constraint) *ObjectConstraint {
	o.deplock.Lock()
	defer o.deplock.Unlock()

	o.schemadeps[from] = c
	return o
}

// GetPropDependencies returns the list of property names that must
// be present for given property name `from`
func (o *ObjectConstraint) GetPropDependencies(from string) []string {
	o.deplock.Lock()
	defer o.deplock.Unlock()

	l, ok := o.propdeps[from]
	if !ok {
		return nil
	}

	return l
}

// GetSchemaDependency returns the Constraint that must be used when
// the property `from` is present.
func (o *ObjectConstraint) GetSchemaDependency(from string) Constraint {
	o.deplock.Lock()
	defer o.deplock.Unlock()

	c, ok := o.schemadeps[from]
	if !ok {
		return nil
	}

	return c
}

// getProps return all of the property names for this object.
// XXX Map keys can be something other than strings, but
// we can't really allow it?
func (o *ObjectConstraint) getPropNames(rv reflect.Value) ([]string, error) {
	var keys []string
	switch rv.Kind() {
	case reflect.Map:
		vk := rv.MapKeys()
		keys = make([]string, len(vk))
		for i, v := range vk {
			if v.Kind() != reflect.String {
				return nil, errors.New("panic: can only handle maps with string keys")
			}
			keys[i] = v.String()
		}
	case reflect.Struct:
		fetcher := o.FieldNamesFromStruct
		if fetcher == nil {
			fetcher = DefaultFieldNamesFromStruct
		}
		if keys = fetcher(rv); keys == nil {
			// Can't happen, because we check for reflect.Struct,
			// but for completeness
			return nil, errors.New("panic: can only handle structs")
		}
	default:
		return nil, errors.New("cannot get property names from this value")
	}

	return keys, nil
}

func (o *ObjectConstraint) getProp(rv reflect.Value, pname string) reflect.Value {
	switch rv.Kind() {
	case reflect.Map:
		pv := reflect.ValueOf(pname)
		return rv.MapIndex(pv)
	case reflect.Struct:
		fetcher := o.FieldIndexFromName
		if fetcher == nil {
			fetcher = DefaultFieldIndexFromName
		}
		i := fetcher(rv, pname)
		if i < 0 {
			return zeroval
		}
		return rv.Field(i)
	default:
		return zeroval
	}
}

// Validate validates the given value against this ObjectConstraint
func (o *ObjectConstraint) Validate(v interface{}) (err error) {
	if pdebug.Enabled {
		g := pdebug.IPrintf("START ObjectConstraint.Validate")
		defer func() {
			if err == nil {
				g.IRelease("END ObjectConstraint.Validate (PASS)")
			} else {
				g.IRelease("END ObjectConstraint.Validate (FAIL): %s", err)
			}
		}()
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		rv = rv.Elem()
	}

	fields, err := o.getPropNames(rv)
	if err != nil {
		return err
	}

	lf := len(fields)
	if o.minProperties > -1 && lf < o.minProperties {
		return errors.New("fewer properties than minProperties")
	}
	if o.maxProperties > -1 && lf > o.maxProperties {
		return errors.New("more properties than maxProperties")
	}

	// Find the list of field names that were passed to us
	// "premain" shows extra props, if any.
	// "pseen" shows props that we have already seen
	premain := map[string]struct{}{}
	pseen := map[string]struct{}{}
	for _, k := range fields {
		premain[k] = struct{}{}
	}

	// Now, for all known constraints, validate the prop
	// create a copy of properties so that we don't have to keep the lock
	propdefs := make(map[string]Constraint)
	o.proplock.Lock()
	for pname, c := range o.properties {
		propdefs[pname] = c
	}
	o.proplock.Unlock()

	for pname, c := range propdefs {
		if pdebug.Enabled {
			pdebug.Printf("Validating property '%s'", pname)
		}

		pval := o.getProp(rv, pname)
		if pval == zeroval {
			if pdebug.Enabled {
				pdebug.Printf("Property '%s' does not exist", pname)
			}
			if o.IsPropRequired(pname) { // required, and not present.
				return errors.New("object property '" + pname + "' is required")
			}

			// At this point we know that the property was not present
			// and that this field was indeed not required.
			if c.HasDefault() {
				// We have default
				dv := c.DefaultValue()
				pval = reflect.ValueOf(dv)
			}

			continue
		}

		// delete from remaining props
		delete(premain, pname)
		// ...and add to props that we have seen
		pseen[pname] = struct{}{}

		if err := c.Validate(pval.Interface()); err != nil {
			return errors.New("object property '" + pname + "' validation failed: " + err.Error())
		}
	}

	for pat, c := range o.patternProperties {
		for pname := range premain {
			if !pat.MatchString(pname) {
				continue
			}
			// No need to check if this pname exists, as we're taking
			// this from "premain"
			pval := o.getProp(rv, pname)

			delete(premain, pname)
			pseen[pname] = struct{}{}
			if err := c.Validate(pval.Interface()); err != nil {
				return errors.New("object property '" + pname + "' validation failed: " + err.Error())
			}
		}
	}

	if len(premain) > 0 {
		c := o.additionalProperties
		if c == nil {
			return errors.New("additional items are not allowed")
		}

		for pname := range premain {
			pval := o.getProp(rv, pname)
			if err := c.Validate(pval.Interface()); err != nil {
				return errors.New("object property for '" + pname + "' validation failed: " + err.Error())
			}
		}
	}

	for pname := range pseen {
		if deps := o.GetPropDependencies(pname); len(deps) > 0 {
			if pdebug.Enabled {
				pdebug.Printf("Property '%s' has dependencies", pname)
			}
			for _, dep := range deps {
				if _, ok := pseen[dep]; !ok {
					return errors.New("required dependency '" + dep + "' is mising")
				}
			}

			// can't, and shouldn't do object validation after checking prop deps
			continue
		}

		if depc := o.GetSchemaDependency(pname); depc != nil {
			if err := depc.Validate(v); err != nil {
				return err
			}
		}
	}

	return nil
}
