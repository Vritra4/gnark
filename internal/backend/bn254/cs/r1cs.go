// Copyright 2020 ConsenSys Software Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by gnark DO NOT EDIT

package cs

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"

	"github.com/fxamacker/cbor/v2"

	"github.com/consensys/gnark/backend/hint"
	"github.com/consensys/gnark/internal/backend/compiled"
	"github.com/consensys/gnark/internal/backend/ioutils"

	"github.com/consensys/gnark-crypto/ecc"
	"text/template"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

type hintFunction func(input []fr.Element) fr.Element

// ErrUnsatisfiedConstraint can be generated when solving a R1CS
var ErrUnsatisfiedConstraint = errors.New("constraint is not satisfied")

// R1CS decsribes a set of R1CS constraint
type R1CS struct {
	compiled.R1CS
	Coefficients []fr.Element // R1C coefficients indexes point here
	loggerOut    io.Writer
	mHints       map[int]int // correspondance between hint wire ID and hint data struct
}

// NewR1CS returns a new R1CS and sets r1cs.Coefficient (fr.Element) from provided big.Int values
func NewR1CS(r1cs compiled.R1CS, coefficients []big.Int) *R1CS {
	r := R1CS{
		R1CS:         r1cs,
		Coefficients: make([]fr.Element, len(coefficients)),
		loggerOut:    os.Stdout,
	}
	for i := 0; i < len(coefficients); i++ {
		r.Coefficients[i].SetBigInt(&coefficients[i])
	}

	r.initHints()

	return &r
}

// GetNbCoefficients return the number of unique coefficients needed in the R1CS
func (r1cs *R1CS) GetNbCoefficients() int {
	return len(r1cs.Coefficients)
}

// CurveID returns curve ID as defined in gnark-crypto (ecc.BN254)
func (r1cs *R1CS) CurveID() ecc.ID {
	return ecc.BN254
}

// FrSize return fr.Limbs * 8, size in byte of a fr element
func (r1cs *R1CS) FrSize() int {
	return fr.Limbs * 8
}

// SetLoggerOutput replace existing logger output with provided one
// default uses os.Stdout
// if nil is provided, logs are not printed
func (r1cs *R1CS) SetLoggerOutput(w io.Writer) {
	r1cs.loggerOut = w
}

// WriteTo encodes R1CS into provided io.Writer using cbor
func (r1cs *R1CS) WriteTo(w io.Writer) (int64, error) {
	_w := ioutils.WriterCounter{W: w} // wraps writer to count the bytes written
	encoder := cbor.NewEncoder(&_w)

	// encode our object
	if err := encoder.Encode(r1cs); err != nil {
		return _w.N, err
	}

	return _w.N, nil
}

// ReadFrom attempts to decode R1CS from io.Reader using cbor
func (r1cs *R1CS) ReadFrom(r io.Reader) (int64, error) {
	dm, err := cbor.DecOptions{MaxArrayElements: 134217728}.DecMode()
	if err != nil {
		return 0, err
	}
	decoder := dm.NewDecoder(r)
	if err := decoder.Decode(&r1cs); err != nil {
		return int64(decoder.NumBytesRead()), err
	}

	// init the hint map
	r1cs.initHints()

	return int64(decoder.NumBytesRead()), nil
}

func (r1cs *R1CS) initHints() {
	// we may do that sooner to save time in the solver, but we want the serialized data structures to be
	// deterministic, hence avoid maps in there.
	r1cs.mHints = make(map[int]int, len(r1cs.Hints))
	for i := 0; i < len(r1cs.Hints); i++ {
		r1cs.mHints[r1cs.Hints[i].WireID] = i
	}
}

// IsSolved returns nil if given witness solves the R1CS and error otherwise
// this method wraps r1cs.Solve() and allocates r1cs.Solve() inputs
func (r1cs *R1CS) IsSolved(witness []fr.Element, hintFunctions []hint.Function) error {
	a := make([]fr.Element, r1cs.NbConstraints)
	b := make([]fr.Element, r1cs.NbConstraints)
	c := make([]fr.Element, r1cs.NbConstraints)
	wireValues := make([]fr.Element, r1cs.NbInternalVariables+r1cs.NbPublicVariables+r1cs.NbSecretVariables)
	return r1cs.Solve(witness, a, b, c, wireValues, hintFunctions)
}

// Solve sets all the wires and returns the a, b, c vectors.
// the r1cs system should have been compiled before. The entries in a, b, c are in Montgomery form.
// witness: contains the input variables
// a, b, c vectors: ab-c = hz
// wireValues =  [publicWires | secretWires | internalWires ]
// witness = [publicWires | secretWires] (without the ONE_WIRE !)
func (r1cs *R1CS) Solve(witness []fr.Element, a, b, c, wireValues []fr.Element, hintFunctions []hint.Function) error {

	if len(witness) != int(r1cs.NbPublicVariables-1+r1cs.NbSecretVariables) { // - 1 for ONE_WIRE
		return fmt.Errorf("invalid witness size, got %d, expected %d = %d (public - ONE_WIRE) + %d (secret)", len(witness), int(r1cs.NbPublicVariables-1+r1cs.NbSecretVariables), r1cs.NbPublicVariables-1, r1cs.NbSecretVariables)
	}

	nbWires := r1cs.NbPublicVariables + r1cs.NbSecretVariables + r1cs.NbInternalVariables

	// compute the wires and the a, b, c polynomials
	if len(a) != int(r1cs.NbConstraints) || len(b) != int(r1cs.NbConstraints) || len(c) != int(r1cs.NbConstraints) || len(wireValues) != nbWires {
		return errors.New("invalid input size: len(a, b, c) == r1cs.NbConstraints and len(wireValues) == r1cs.NbWires")
	}

	// keep track of wire that have a value
	wireInstantiated := make([]bool, nbWires)
	wireInstantiated[0] = true // ONE_WIRE
	wireValues[0].SetOne()
	copy(wireValues[1:], witness) // TODO factorize
	for i := 0; i < len(witness); i++ {
		wireInstantiated[i+1] = true
	}

	// now that we know all inputs are set, defer log printing once all wireValues are computed
	// (or sooner, if a constraint is not satisfied)
	defer r1cs.printLogs(wireValues, wireInstantiated)

	// init hint functions data structs
	mHintsFunctions := make(map[hint.ID]hintFunction, len(hintFunctions)+2)
	mHintsFunctions[hint.IsZero] = powModulusMinusOne
	mHintsFunctions[hint.IthBit] = ithBit

	for i := 0; i < len(hintFunctions); i++ {
		if _, ok := mHintsFunctions[hintFunctions[i].ID]; ok {
			return fmt.Errorf("duplicate hint function with id %d", uint32(hintFunctions[i].ID))
		}
		f, ok := hintFunctions[i].F.(hintFunction)
		if !ok {
			return fmt.Errorf("invalid hint function signature with id %d", uint32(hintFunctions[i].ID))
		}
		mHintsFunctions[hintFunctions[i].ID] = f
	}

	// check if there is an inconsistant constraint
	var check fr.Element

	// this variable is used to navigate in the debugInfoComputation slice.
	// It is incremented by one each time a division happens for solving a constraint.
	var debugInfoComputationOffset uint

	// Loop through computational constraints (the one wwe need to solve and compute a wire in)
	for i := 0; i < int(r1cs.NbCOConstraints); i++ {

		// solve the constraint, this will compute the missing wire of the gate
		debugInfoComputationOffset += r1cs.solveR1C(&r1cs.Constraints[i], wireInstantiated, wireValues, mHintsFunctions)

		// at this stage we are guaranteed that a[i]*b[i]=c[i]
		// if not, it means there is a bug in the solver
		a[i], b[i], c[i] = instantiateR1C(&r1cs.Constraints[i], r1cs, wireValues)

		check.Mul(&a[i], &b[i])
		if !check.Equal(&c[i]) {
			//return fmt.Errorf("%w: %s", ErrUnsatisfiedConstraint, "couldn't solve computational constraint. May happen: div by 0 or no inverse found")
			debugInfo := r1cs.DebugInfoComputation[debugInfoComputationOffset]
			debugInfoStr := r1cs.logValue(debugInfo, wireValues, wireInstantiated)
			return fmt.Errorf("%w: %s", ErrUnsatisfiedConstraint, debugInfoStr)
		}
	}

	// Loop through the assertions -- here all wireValues should be instantiated
	// if a[i] * b[i] != c[i]; it means the constraint is not satisfied
	for i := int(r1cs.NbCOConstraints); i < len(r1cs.Constraints); i++ {

		// A this stage we are not guaranteed that a[i+sizecg]*b[i+sizecg]=c[i+sizecg] because we only query the values (computed
		// at the previous step)
		a[i], b[i], c[i] = instantiateR1C(&r1cs.Constraints[i], r1cs, wireValues)

		// check that the constraint is satisfied
		check.Mul(&a[i], &b[i])
		if !check.Equal(&c[i]) {
			debugInfo := r1cs.DebugInfoAssertion[i-int(r1cs.NbCOConstraints)]
			debugInfoStr := r1cs.logValue(debugInfo, wireValues, wireInstantiated)
			return fmt.Errorf("%w: %s", ErrUnsatisfiedConstraint, debugInfoStr)
		}
	}

	// TODO @gbotrel ensure all wires are marked as "instantiated"

	return nil
}

func (r1cs *R1CS) logValue(entry compiled.LogEntry, wireValues []fr.Element, wireInstantiated []bool) string {
	var toResolve []interface{}
	for j := 0; j < len(entry.ToResolve); j++ {
		wireID := entry.ToResolve[j]
		if !wireInstantiated[wireID] {
			toResolve = append(toResolve, "???")
		} else {
			toResolve = append(toResolve, wireValues[wireID].String())
		}
	}
	return fmt.Sprintf(entry.Format, toResolve...)
}

func (r1cs *R1CS) printLogs(wireValues []fr.Element, wireInstantiated []bool) {

	// for each log, resolve the wire values and print the log to stdout
	for i := 0; i < len(r1cs.Logs); i++ {
		logLine := r1cs.logValue(r1cs.Logs[i], wireValues, wireInstantiated)
		if r1cs.loggerOut != nil {
			if _, err := io.WriteString(r1cs.loggerOut, logLine); err != nil {
				fmt.Println("error", err.Error())
			}
		}
	}
}

// AddTerm returns res += (value * term.Coefficient)
func (r1cs *R1CS) AddTerm(res *fr.Element, t compiled.Term, value fr.Element) *fr.Element {
	cID := t.CoeffID()
	switch cID {
	case compiled.CoeffIdOne:
		return res.Add(res, &value)
	case compiled.CoeffIdMinusOne:
		return res.Sub(res, &value)
	case compiled.CoeffIdZero:
		return res
	case compiled.CoeffIdTwo:
		var buffer fr.Element
		buffer.Double(&value)
		return res.Add(res, &buffer)
	default:
		var buffer fr.Element
		buffer.Mul(&r1cs.Coefficients[cID], &value)
		return res.Add(res, &buffer)
	}
}

// mulWireByCoeff returns into.Mul(into, term.Coefficient)
func (r1cs *R1CS) mulWireByCoeff(res *fr.Element, t compiled.Term) *fr.Element {
	cID := t.CoeffID()
	switch cID {
	case compiled.CoeffIdOne:
		return res
	case compiled.CoeffIdMinusOne:
		return res.Neg(res)
	case compiled.CoeffIdZero:
		return res.SetZero()
	case compiled.CoeffIdTwo:
		return res.Double(res)
	default:
		return res.Mul(res, &r1cs.Coefficients[cID])
	}
}

// compute left, right, o part of a r1cs constraint
// this function is called when all the wires have been computed
// it instantiates the l, r o part of a R1C
func instantiateR1C(r *compiled.R1C, r1cs *R1CS, wireValues []fr.Element) (a, b, c fr.Element) {

	for _, t := range r.L {
		r1cs.AddTerm(&a, t, wireValues[t.VariableID()])
	}

	for _, t := range r.R {
		r1cs.AddTerm(&b, t, wireValues[t.VariableID()])
	}

	for _, t := range r.O {
		r1cs.AddTerm(&c, t, wireValues[t.VariableID()])
	}

	return
}

// solveR1c computes a wire by solving a r1cs
// the function searches for the unset wire (either the unset wire is
// alone, or it can be computed without ambiguity using the other computed wires
// , eg when doing a binary decomposition: either way the missing wire can
// be computed without ambiguity because the r1cs is correctly ordered)
//
// It returns the 1 if the the position to solve is in the quadratic part (it
// means that there is a division and serves to navigate in the log info for the
// computational constraints), and 0 otherwise.
func (r1cs *R1CS) solveR1C(r *compiled.R1C, wireInstantiated []bool, wireValues []fr.Element, mHintsFunctions map[hint.ID]hintFunction) uint {

	// value to return: 1 if the wire to solve is in the quadratic term, 0 otherwise
	var offset uint

	// the index of the non zero entry shows if L, R or O has an uninstantiated wire
	// the content is the ID of the wire non instantiated
	var loc uint8

	var a, b, c fr.Element
	var termToCompute compiled.Term

	processTerm := func(t compiled.Term, val *fr.Element, locValue uint8) {
		vID := t.VariableID()

		// wire is already computed, we just accumulate in val
		if wireInstantiated[vID] {
			r1cs.AddTerm(val, t, wireValues[vID])
			return
		}

		// first we check if this is a hint wire
		if hID, ok := r1cs.mHints[vID]; ok {
			// compute hint value
			hint := r1cs.Hints[hID]

			// compute values for all inputs.
			inputs := make([]fr.Element, len(hint.Inputs))
			for i := 0; i < len(hint.Inputs); i++ {
				// input is a linear expression, we must compute the value
				for j := 0; j < len(hint.Inputs[i]); j++ {
					viID := hint.Inputs[i][j].VariableID()
					if !wireInstantiated[viID] {
						// TODO @gbotrel return error here
						panic("input not instantiated for hint function")
					}
					r1cs.AddTerm(&inputs[i], hint.Inputs[i][j], wireValues[viID])
				}
			}

			f, ok := mHintsFunctions[hint.ID]
			if !ok {
				panic("missing hint function")
			}
			wireValues[vID] = f(inputs)
			wireInstantiated[vID] = true
			return
		}

		if loc != 0 {
			panic("found more than one wire to instantiate")
		}
		termToCompute = t
		loc = locValue
	}

	for _, t := range r.L {
		processTerm(t, &a, 1)
	}

	for _, t := range r.R {
		processTerm(t, &b, 2)
	}

	for _, t := range r.O {
		processTerm(t, &c, 3)
	}

	// ensure we found the unset wire
	if loc == 0 {
		// this wire may have been instantiated as part of moExpression already
		return 0
	}

	// we compute the wire value and instantiate it
	vID := termToCompute.VariableID()

	switch loc {
	case 1:
		if !b.IsZero() {
			wireValues[vID].Div(&c, &b).
				Sub(&wireValues[vID], &a)
			r1cs.mulWireByCoeff(&wireValues[vID], termToCompute)
			offset = 1
		}
	case 2:
		if !a.IsZero() {
			wireValues[vID].Div(&c, &a).
				Sub(&wireValues[vID], &b)
			r1cs.mulWireByCoeff(&wireValues[vID], termToCompute)
			offset = 1
		}
	case 3:
		wireValues[vID].Mul(&a, &b).
			Sub(&wireValues[vID], &c)
		r1cs.mulWireByCoeff(&wireValues[vID], termToCompute)
	}

	wireInstantiated[vID] = true

	return offset
}

// default hint functions

func powModulusMinusOne(inputs []fr.Element) (v fr.Element) {
	if len(inputs) != 1 {
		panic("expected one input")
	}
	var eOne big.Int
	eOne.SetUint64(1)
	eOne.Sub(fr.Modulus(), &eOne)
	v.Exp(inputs[0], &eOne)
	one := fr.One()
	v.Sub(&one, &v)
	return v
}

func ithBit(inputs []fr.Element) (v fr.Element) {
	if len(inputs) != 2 {
		panic("expected 2 inputs; inputs[0] == value, inputs[1] == bit position")
	}
	// TODO @gbotrel this is very inneficient; it adds ~256*2 multiplications to extract all bits of a value.
	inputs[0].FromMont()
	inputs[1].FromMont()
	if !inputs[1].IsUint64() {
		panic("expected bit position to fit on one word")
	}
	v.SetUint64(inputs[0].Bit(inputs[1][0]))

	return v
}

// ToHTML returns an HTML human-readable representation of the constraint system
func (r1cs *R1CS) ToHTML(w io.Writer) error {
	t, err := template.New("r1cs.html").Funcs(template.FuncMap{
		"toHTML": toHTML,
		"add":    add,
		"sub":    sub,
	}).Parse(compiled.R1CSTemplate)
	if err != nil {
		return err
	}

	type data struct {
		*R1CS
		MHints map[int]int
	}
	d := data{
		r1cs,
		r1cs.mHints,
	}
	return t.Execute(w, &d)
}

func add(a, b int) int {
	return a + b
}

func sub(a, b int) int {
	return a - b
}

func toHTML(l compiled.LinearExpression, coeffs []fr.Element, mHints map[int]int) string {
	var sbb strings.Builder
	for i := 0; i < len(l); i++ {
		termToHTML(l[i], &sbb, coeffs, mHints, false)
		if i+1 < len(l) {
			sbb.WriteString(" + ")
		}
	}
	return sbb.String()
}

func termToHTML(t compiled.Term, sbb *strings.Builder, coeffs []fr.Element, mHints map[int]int, offset bool) {
	tID := t.CoeffID()
	if tID == compiled.CoeffIdOne {
		// do nothing, just print the variable
	} else if tID == compiled.CoeffIdMinusOne {
		// print neg sign
		sbb.WriteString("<span class=\"coefficient\">-</span>")
	} else if tID == compiled.CoeffIdZero {
		sbb.WriteString("<span class=\"coefficient\">0</span>")
		return
	} else {
		sbb.WriteString("<span class=\"coefficient\">")
		sbb.WriteString(coeffs[tID].String())
		sbb.WriteString("</span>*")
	}

	vID := t.VariableID()
	class := ""
	switch t.VariableVisibility() {
	case compiled.Internal:
		class = "internal"
		if _, ok := mHints[vID]; ok {
			class = "hint"
		}
	case compiled.Public:
		class = "public"
	case compiled.Secret:
		class = "secret"
	case compiled.Virtual:
		class = "virtual"
	case compiled.Unset:
		class = "unset"
	default:
		panic("not implemented")
	}
	if offset {
		vID++ // for sparse R1CS, we offset to have same variable numbers as in R1CS
	}
	sbb.WriteString(fmt.Sprintf("<span class=\"%s\">v%d</span>", class, vID))

}
