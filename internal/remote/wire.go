package remote

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const (
	wHello       uint8 = 1
	wPair        uint8 = 2
	wAuth        uint8 = 3
	wResume      uint8 = 4
	wList        uint8 = 5
	wWatch       uint8 = 6
	wSize        uint8 = 7
	wScroll      uint8 = 8
	wText        uint8 = 9
	wKeys        uint8 = 10
	wUnwatch     uint8 = 11
	wChallenge   uint8 = 12
	wPairCode    uint8 = 13
	wAuthed      uint8 = 14
	wSessions    uint8 = 15
	wTheme       uint8 = 16
	wFrame       uint8 = 17
	wExited      uint8 = 18
	wError       uint8 = 19
	wWheel       uint8 = 20
	wSetTheme    uint8 = 21
	wNewTerm     uint8 = 22
	wNewTermDone uint8 = 23
	wPushSub     uint8 = 24
	wImage       uint8 = 25
	wSchedule       uint8 = 26
	wScheduleDone   uint8 = 27
	wScheduleList   uint8 = 28
	wScheduleJobs   uint8 = 29
	wScheduleCancel uint8 = 30
)

// wireProto is the protocol version sent in Challenge and version.json.
const wireProto = 1

const maxPayloadSize = 1024 * 1024 // 1 MiB

// preAuthMaxPayload caps the frames read before authentication: Hello,
// Auth, Resume and Pair are a few KiB at most.
const preAuthMaxPayload = 64 << 10

type b64Bytes []byte

func (b b64Bytes) MarshalJSON() ([]byte, error) {
	if b == nil {
		return json.Marshal("")
	}
	return json.Marshal(base64.RawURLEncoding.EncodeToString(b))
}

func (b *b64Bytes) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		*b = nil
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(s)
		if err != nil {
			return err
		}
	}
	*b = raw
	return nil
}

type HelloMsg struct {
	Proto int `json:"proto"`
}

type RegistrationWire struct {
	CredID     b64Bytes `json:"cred_id"`
	PubKey     b64Bytes `json:"pub_key"`
	AuthData   b64Bytes `json:"auth_data"`
	ClientData b64Bytes `json:"client_data"`
	Name       string   `json:"name"`
}

func (rw RegistrationWire) toRegistration() Registration {
	return Registration{
		CredID:     rw.CredID,
		PubKey:     rw.PubKey,
		AuthData:   rw.AuthData,
		ClientData: rw.ClientData,
		Name:       rw.Name,
	}
}

type PairMsg struct {
	Token      b64Bytes         `json:"token"`
	Reg        RegistrationWire `json:"reg"`
	SessionPub b64Bytes         `json:"session_pub"`
}

type AssertionWire struct {
	CredID     b64Bytes `json:"cred_id"`
	AuthData   b64Bytes `json:"auth_data"`
	ClientData b64Bytes `json:"client_data"`
	Sig        b64Bytes `json:"sig"`
}

func (aw AssertionWire) toAssertion() Assertion {
	return Assertion{
		CredID:     aw.CredID,
		AuthData:   aw.AuthData,
		ClientData: aw.ClientData,
		Sig:        aw.Sig,
	}
}

type AuthMsg struct {
	Assertion  AssertionWire `json:"assertion"`
	SessionPub b64Bytes      `json:"session_pub"`
}

type ResumeMsg struct {
	ID    string   `json:"id"`
	Sig   b64Bytes `json:"sig"`
	Nonce b64Bytes `json:"nonce,omitempty"` // the Challenge nonce signed; empty: this connection's
}

// WatchMsg selects a terminal. Peek only shows it: it keeps its owner and its
// size until a Watch without Peek or an input takes it.
type WatchMsg struct {
	Key  string `json:"key"`
	Peek bool   `json:"peek,omitempty"`
}

type SizeMsg struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
	CW   int `json:"cw,omitempty"`
	CH   int `json:"ch,omitempty"`
}

type ScrollMsg struct {
	Offset int `json:"offset"`
}

// WheelMsg is the phone's scroll gesture over a full-screen program (the
// frame said Alt or Mouse): Steps wheel notches, positive toward the
// history, over cell X, Y.
type WheelMsg struct {
	Steps int `json:"steps"`
	X     int `json:"x"`
	Y     int `json:"y"`
}

// NewTermDoneMsg answers NewTerm (empty payload) with the new session,
// already in the Sessions list sent just before.
type NewTermDoneMsg struct {
	Key string `json:"key"`
}

type TextMsg struct {
	Text  string `json:"text"`
	Enter bool   `json:"enter"`
}

type KeysMsg struct {
	Names []string `json:"names"`
}

type ScheduleMsg struct {
	Pane string `json:"pane,omitempty"`
	At   string `json:"at"`
	Text string `json:"text"`
}

type ScheduleDoneMsg struct {
	ID    string `json:"id,omitempty"`
	Pane  string `json:"pane,omitempty"`
	RunAt string `json:"run_at,omitempty"`
	Desc  string `json:"desc,omitempty"`
	Error string `json:"error,omitempty"`
}

type ScheduleJob struct {
	ID       string `json:"id"`
	Pane     string `json:"pane"`
	PaneName string `json:"pane_name,omitempty"`
	RunAt    string `json:"run_at"`
	In       string `json:"in"`
	Desc     string `json:"desc"`
}

type ScheduleListMsg struct {
	Pane string `json:"pane,omitempty"`
}

type ScheduleJobsMsg struct {
	Pane string        `json:"pane,omitempty"`
	Jobs []ScheduleJob `json:"jobs"`
}

type ScheduleCancelMsg struct {
	ID string `json:"id"`
}

type ChallengeMsg struct {
	Proto int      `json:"proto"`
	Nonce b64Bytes `json:"nonce"`
}

type PairCodeMsg struct {
	Code string `json:"code"`
}

type AuthedMsg struct {
	DeviceID string      `json:"device_id"`
	ResumeID string      `json:"resume_id"`
	Hashes   []CertHash  `json:"hashes"`
	Addrs    []string    `json:"addrs,omitempty"` // where the machine listens now
	Themes   []ThemeInfo `json:"themes"`          // what SetTheme accepts
	Push     bool        `json:"push"`            // the machine takes PushSub
}

// ThemeInfo names one of neonmux's themes for the phone's picker.
type ThemeInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// SetThemeMsg picks the connection's theme: a ThemeInfo name, or "" to
// follow the desktop's.
type SetThemeMsg struct {
	Name string `json:"name"`
}

type SessionsMsg struct {
	List []SessionInfo `json:"list"`
}

type ThemeMsg struct {
	FG     [3]uint8 `json:"fg"`
	BG     [3]uint8 `json:"bg"`
	Cursor [3]uint8 `json:"cursor"`
	// The rest is for the phone's interface.
	Accent  [3]uint8     `json:"accent"`
	Accent2 [3]uint8     `json:"accent2"`
	Ansi    [16][3]uint8 `json:"ansi"`
}

func themeMsg(p Palette) ThemeMsg {
	tm := ThemeMsg{FG: p.FG, BG: p.BG, Cursor: p.Cursor, Accent: p.Accent, Accent2: p.Accent2}
	copy(tm.Ansi[:], p.P[:16])
	return tm
}

type FrameLine struct {
	Y    int
	Runs []Run
}

func (fl FrameLine) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{fl.Y, fl.Runs})
}

func (fl *FrameLine) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != 2 {
		return errors.New("invalid frame line array length")
	}
	if err := json.Unmarshal(raw[0], &fl.Y); err != nil {
		return err
	}
	return json.Unmarshal(raw[1], &fl.Runs)
}

func (r Run) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{r.Text, r.FG, r.BG, r.Attr})
}

func (r *Run) UnmarshalJSON(data []byte) error {
	var arr []any
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	if len(arr) != 4 {
		return errors.New("invalid run array length")
	}
	text, ok := arr[0].(string)
	if !ok {
		return errors.New("invalid run text")
	}
	fg, ok := arr[1].(float64)
	if !ok {
		return errors.New("invalid run fg")
	}
	bg, ok := arr[2].(float64)
	if !ok {
		return errors.New("invalid run bg")
	}
	attr, ok := arr[3].(float64)
	if !ok {
		return errors.New("invalid run attr")
	}
	r.Text = text
	r.FG = int32(fg)
	r.BG = int32(bg)
	r.Attr = uint8(attr)
	return nil
}

// ImgRect is an image drawn over the grid: X, Y, W, H in cells of the view,
// SX, SY, SW, SH the part of the image shown, in pixels of the image at its
// reference size (imageRefSize, ImageMsg.W x H). On the wire, an array
// [id, gen, x, y, w, h, sx, sy, sw, sh] like Run.
type ImgRect struct {
	ID  uint32
	Gen uint64
	X   float64
	Y   float64
	W   float64
	H   float64
	SX  int
	SY  int
	SW  int
	SH  int
}

func (ir ImgRect) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{ir.ID, ir.Gen, ir.X, ir.Y, ir.W, ir.H, ir.SX, ir.SY, ir.SW, ir.SH})
}

func (ir *ImgRect) UnmarshalJSON(data []byte) error {
	var a []float64
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	if len(a) != 10 {
		return errors.New("invalid imgrect array length")
	}
	*ir = ImgRect{ID: uint32(a[0]), Gen: uint64(a[1]), X: a[2], Y: a[3], W: a[4], H: a[5],
		SX: int(a[6]), SY: int(a[7]), SW: int(a[8]), SH: int(a[9])}
	return nil
}

// ImageMsg carries an image of session Key, sent after the first frame
// that shows it, and again only once the phone dropped it (phoneLRU). W x H
// is its reference size, the one the ImgRect sources refer to; Data (JPEG
// or PNG, at most maxImagePayload bytes) may be smaller, the phone scales
// the sources by its real size. View changes each time the daemon starts
// over for this phone (Watch, Unwatch, Exited): the phone does the same.
type ImageMsg struct {
	Key  string   `json:"key"`
	View int      `json:"view"`
	ID   uint32   `json:"id"`
	Gen  uint64   `json:"gen"`
	W    int      `json:"w"`
	H    int      `json:"h"`
	Data b64Bytes `json:"data"`
}

type FrameMsg struct {
	Cols   int         `json:"cols"`
	Rows   int         `json:"rows"`
	Offset int         `json:"offset"`
	Hist   int         `json:"hist"`
	CX     int         `json:"cx"`
	CY     int         `json:"cy"`
	Cursor bool        `json:"cursor"`
	Full   bool        `json:"full"`
	Alt    bool        `json:"alt,omitempty"`   // alternate screen
	Mouse  bool        `json:"mouse,omitempty"` // the program tracks the mouse
	Lines  []FrameLine `json:"lines"`
	Imgs   []ImgRect   `json:"imgs,omitempty"`
}

type ErrorMsg struct {
	Msg string `json:"msg"`
	Min int    `json:"min,omitempty"`
	Max int    `json:"max,omitempty"`
}

// errPayloadTooBig is writeFrame's refusal of a payload over
// maxPayloadSize: nothing is written, the stream stays usable.
var errPayloadTooBig = errors.New("payload exceeds 1 MiB limit")

func writeFrame(w io.Writer, typ uint8, payload any) error {
	var payloadBytes []byte
	if payload != nil {
		if b, ok := payload.([]byte); ok {
			payloadBytes = b
		} else {
			var err error
			payloadBytes, err = json.Marshal(payload)
			if err != nil {
				return err
			}
		}
	}
	if len(payloadBytes) > maxPayloadSize {
		return errPayloadTooBig
	}
	buf := make([]byte, 5+len(payloadBytes))
	buf[0] = typ
	binary.LittleEndian.PutUint32(buf[1:5], uint32(len(payloadBytes)))
	copy(buf[5:], payloadBytes)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) (uint8, []byte, error) {
	return readFrameMax(r, func() uint32 { return maxPayloadSize })
}

// readFrameMax reads a frame whose payload may not exceed limit() bytes,
// asked once the header is in: the limit may change while it waits.
func readFrameMax(r io.Reader, limit func() uint32) (uint8, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	typ := header[0]
	length := binary.LittleEndian.Uint32(header[1:5])
	if length > limit() {
		return typ, nil, errors.New("payload length exceeds the frame limit")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return typ, nil, err
	}
	return typ, payload, nil
}
