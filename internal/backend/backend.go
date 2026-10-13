// Package backend abstracts outputs and input devices so the compositor runs
// headless, nested or on DRM.
package backend

import (
	"fmt"
	"image"
	"time"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/render"
)

func Now() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		panic(fmt.Sprintf("backend: clock_gettime(CLOCK_MONOTONIC) cannot fail with valid arguments: %v", err))
	}
	return time.Duration(ts.Nano())
}

type Backend interface {
	Start(h Handler) error
	Close() error
}

type Handler interface {
	OutputAdded(o Output) error
	OutputRemoved(o Output) error
	Frame(o Output) error
	Presented(o Output, p Presentation) error
	DeviceAdded(d Device) error
	DeviceRemoved(d Device) error
	Input(d Device, e Event) error
}

type Mode struct {
	Size       image.Point
	RefreshMHz int32
	Preferred  bool
}

func (m Mode) Valid() error {
	if m.Size.X <= 0 || m.Size.Y <= 0 || m.RefreshMHz <= 0 {
		return fmt.Errorf("backend: invalid mode %dx%d@%dmHz", m.Size.X, m.Size.Y, m.RefreshMHz)
	}
	return nil
}

func (m Mode) Interval() time.Duration {
	return time.Duration(int64(time.Second) * 1000 / int64(m.RefreshMHz))
}

type OutputState struct {
	Enabled bool
	Mode    Mode
}

type Presentation struct {
	Time    time.Duration
	Refresh time.Duration
	Seq     uint64
}

type Output interface {
	Name() string
	Description() string
	PhysicalSize() image.Point
	Modes() []Mode
	State() OutputState
	Commit(s OutputState) error
	ScheduleFrame() error
	BeginFrame() (render.Renderer, render.Target, error)
	Present(damage render.Region) error
}

type Capability uint8

const (
	Keyboard Capability = 1 << iota
	Pointer
	Touch
)

func (c Capability) Has(flag Capability) bool {
	return c&flag == flag
}

type Device interface {
	Name() string
	Capabilities() Capability
}

type Event interface {
	Timestamp() time.Duration
}

type Key struct {
	Time    time.Duration
	Code    uint32
	Pressed bool
}

type PointerMotion struct {
	Time                 time.Duration
	DX, DY               float64
	UnaccelDX, UnaccelDY float64
}

type PointerMotionAbsolute struct {
	Time   time.Duration
	Output Output
	X, Y   float64
}

type PointerButton struct {
	Time    time.Duration
	Button  uint32
	Pressed bool
}

type Orientation uint8

const (
	Vertical Orientation = iota
	Horizontal
)

type AxisSource uint8

const (
	Wheel AxisSource = iota
	Finger
	Continuous
	WheelTilt
)

type PointerAxis struct {
	Time        time.Duration
	Orientation Orientation
	Source      AxisSource
	Value       float64
	Value120    int32
}

type PointerFrame struct {
	Time time.Duration
}

type TouchDown struct {
	Time   time.Duration
	ID     int32
	Output Output
	X, Y   float64
}

type TouchMotion struct {
	Time time.Duration
	ID   int32
	X, Y float64
}

type TouchUp struct {
	Time time.Duration
	ID   int32
}

type TouchCancel struct {
	Time time.Duration
	ID   int32
}

type TouchFrame struct {
	Time time.Duration
}

func (e Key) Timestamp() time.Duration                   { return e.Time }
func (e PointerMotion) Timestamp() time.Duration         { return e.Time }
func (e PointerMotionAbsolute) Timestamp() time.Duration { return e.Time }
func (e PointerButton) Timestamp() time.Duration         { return e.Time }
func (e PointerAxis) Timestamp() time.Duration           { return e.Time }
func (e PointerFrame) Timestamp() time.Duration          { return e.Time }
func (e TouchDown) Timestamp() time.Duration             { return e.Time }
func (e TouchMotion) Timestamp() time.Duration           { return e.Time }
func (e TouchUp) Timestamp() time.Duration               { return e.Time }
func (e TouchCancel) Timestamp() time.Duration           { return e.Time }
func (e TouchFrame) Timestamp() time.Duration            { return e.Time }
