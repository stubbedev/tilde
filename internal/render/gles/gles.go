// Package gles is the GLES2 renderer: shm textures, premultiplied blending,
// and passes into framebuffers backed by textures or dmabufs.
package gles

import (
	"cmp"
	"errors"
	"fmt"
	"image"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stubbedev/tilde/internal/gpu"
	"github.com/stubbedev/tilde/internal/render"
)

const vertexShader = `attribute vec2 pos;
attribute vec2 texcoord;
uniform vec2 size;
varying vec2 v_texcoord;
void main() {
	gl_Position = vec4(pos / size * 2.0 - 1.0, 0.0, 1.0);
	v_texcoord = texcoord;
}`

const (
	solidShader = `precision mediump float;
uniform vec4 color;
void main() { gl_FragColor = color; }`
	rgbaShader = `precision mediump float;
uniform sampler2D tex;
uniform float alpha;
varying vec2 v_texcoord;
void main() { gl_FragColor = texture2D(tex, v_texcoord) * alpha; }`
	externalShader = `#extension GL_OES_EGL_image_external : require
precision mediump float;
uniform samplerExternalOES tex;
uniform float alpha;
varying vec2 v_texcoord;
void main() { gl_FragColor = texture2D(tex, v_texcoord) * alpha; }`
	rgbxShader = `precision mediump float;
uniform sampler2D tex;
uniform float alpha;
varying vec2 v_texcoord;
void main() { gl_FragColor = vec4(texture2D(tex, v_texcoord).rgb, 1.0) * alpha; }`
)

var requiredGL = []string{"GL_EXT_texture_format_BGRA8888", "GL_EXT_unpack_subimage", "GL_OES_EGL_image"}

type program struct {
	id                 uint32
	pos, texcoord      int32
	size, color, alpha int32
}

type Renderer struct {
	d         *gpu.Display
	gl        *gpu.GL
	solid     program
	rgba      program
	rgbx      program
	external  program
	modifiers map[uint32][]gpu.Modifier
	vbo       uint32
	verts     [16]float32
	pass      pass
	closed    bool
}

func (r *Renderer) enter() error {
	runtime.LockOSThread()
	if err := r.d.MakeCurrent(); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	return nil
}

func (r *Renderer) leave(op string) error {
	err := errors.Join(r.gl.Check(op), r.d.Release())
	runtime.UnlockOSThread()
	return err
}

func New(d *gpu.Display) (*Renderer, error) {
	r := &Renderer{d: d, gl: d.GL(), modifiers: map[uint32][]gpu.Modifier{}}
	r.pass.owner = r
	if err := r.enter(); err != nil {
		return nil, err
	}
	err := r.setup()
	return r, errors.Join(err, r.leave("gles: set up"))
}

func (r *Renderer) setup() error {
	exts := strings.Fields(r.gl.Extensions())
	for _, ext := range requiredGL {
		if !slices.Contains(exts, ext) {
			return fmt.Errorf("gles: %s lacks %s", r.gl.Renderer(), ext)
		}
	}
	programs := []struct {
		prog     *program
		fragment string
	}{{&r.solid, solidShader}, {&r.rgba, rgbaShader}, {&r.rgbx, rgbxShader}}
	if slices.Contains(exts, "GL_OES_EGL_image_external") {
		programs = append(programs, struct {
			prog     *program
			fragment string
		}{&r.external, externalShader})
	}
	for _, p := range programs {
		id, err := r.gl.Link(vertexShader, p.fragment)
		if err != nil {
			return err
		}
		*p.prog = program{
			id:       id,
			pos:      r.gl.GetAttribLocation(id, "pos"),
			texcoord: r.gl.GetAttribLocation(id, "texcoord"),
			size:     r.gl.GetUniformLocation(id, "size"),
			color:    r.gl.GetUniformLocation(id, "color"),
			alpha:    r.gl.GetUniformLocation(id, "alpha"),
		}
		r.gl.UseProgram(id)
		r.gl.Uniform1i(r.gl.GetUniformLocation(id, "tex"), 0)
	}
	r.gl.GenBuffers(1, &r.vbo)
	return nil
}

func (r *Renderer) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if err := r.enter(); err != nil {
		return err
	}
	for _, p := range []program{r.solid, r.rgba, r.rgbx, r.external} {
		if p.id != 0 {
			r.gl.DeleteProgram(p.id)
		}
	}
	r.gl.DeleteBuffers(1, &r.vbo)
	return r.leave("gles: close")
}

func (r *Renderer) Finish() error {
	if err := r.enter(); err != nil {
		return err
	}
	r.gl.Finish()
	return r.leave("gles: finish")
}

func (r *Renderer) Formats() []render.Format {
	return []render.Format{render.ARGB8888, render.XRGB8888}
}

type texture struct {
	r      *Renderer
	id     uint32
	size   image.Point
	opaque bool
	target uint32
	img    *gpu.Image
}

func fits(size image.Point, stride int, data []byte) error {
	if size.X <= 0 || size.Y <= 0 || stride < size.X*4 || stride%4 != 0 || len(data) < stride*(size.Y-1)+size.X*4 {
		return fmt.Errorf("gles: %v pixels with stride %d do not fit %d bytes", size, stride, len(data))
	}
	return nil
}

func (r *Renderer) NewTexture(format render.Format, size image.Point, stride int, data []byte) (render.Texture, error) {
	if !slices.Contains(r.Formats(), format) {
		return nil, fmt.Errorf("gles: unsupported texture format %d", format)
	}
	if err := fits(size, stride, data); err != nil {
		return nil, err
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	t := &texture{r: r, size: size, opaque: format == render.XRGB8888, target: gpu.Texture2D}
	r.gl.GenTextures(1, &t.id)
	r.gl.BindTexture(gpu.Texture2D, t.id)
	r.gl.TexParameteri(gpu.Texture2D, gpu.TextureWrapS, gpu.ClampToEdge)
	r.gl.TexParameteri(gpu.Texture2D, gpu.TextureWrapT, gpu.ClampToEdge)
	r.gl.PixelStorei(gpu.UnpackRowLengthExt, int32(stride/4))                                    //nolint:gosec // bounded by fits
	r.gl.TexImage2D(gpu.Texture2D, gpu.BGRAExt, int32(size.X), int32(size.Y), gpu.BGRAExt, data) //nolint:gosec // texture sizes fit GLsizei
	r.gl.PixelStorei(gpu.UnpackRowLengthExt, 0)
	if err := r.leave("gles: new texture"); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *texture) Size() image.Point {
	return t.size
}

func (r *Renderer) externalOnly(b gpu.DMABUF) (bool, error) {
	mods, ok := r.modifiers[b.Format]
	if !ok {
		var err error
		if mods, err = r.d.DMABUFModifiers(b.Format); err != nil {
			return false, err
		}
		r.modifiers[b.Format] = mods
	}
	for _, m := range mods {
		if m.Modifier == b.Modifier {
			return m.ExternalOnly, nil
		}
	}
	return false, nil
}

func (r *Renderer) DMABUFFormats() ([]gpu.FormatModifier, error) {
	formats, err := r.d.DMABUFFormats()
	if err != nil {
		return nil, err
	}
	var out []gpu.FormatModifier
	for _, f := range formats {
		mods, err := r.d.DMABUFModifiers(f)
		if err != nil {
			return nil, err
		}
		r.modifiers[f] = mods
		out = append(out, gpu.FormatModifier{Format: f, Modifier: gpu.ModifierInvalid})
		for _, m := range mods {
			out = append(out, gpu.FormatModifier{Format: f, Modifier: m.Modifier})
		}
	}
	slices.SortFunc(out, func(a, b gpu.FormatModifier) int {
		if a.Format != b.Format {
			return cmp.Compare(a.Format, b.Format)
		}
		return cmp.Compare(a.Modifier, b.Modifier)
	})
	return slices.Compact(out), nil
}

func (r *Renderer) MainDevice() (uint64, error) {
	dev, err := r.d.Device()
	if err != nil {
		return 0, err
	}
	node := dev.RenderNode()
	if node == "" {
		return 0, fmt.Errorf("%w: %s has no render node", gpu.ErrUnsupported, r.gl.Renderer())
	}
	var st unix.Stat_t
	if err := unix.Stat(node, &st); err != nil {
		return 0, fmt.Errorf("gles: stat %s: %w", node, err)
	}
	return st.Rdev, nil
}

func (r *Renderer) CheckDMABUF(b gpu.DMABUF) error {
	t, err := r.ImportTexture(b)
	if err != nil {
		return err
	}
	return t.Destroy()
}

func (r *Renderer) ImportTexture(b gpu.DMABUF) (render.Texture, error) {
	external, err := r.externalOnly(b)
	if err != nil {
		return nil, err
	}
	if external && r.external.id == 0 {
		return nil, fmt.Errorf("%w: %s samples format %#x with modifier %#x only through GL_OES_EGL_image_external", gpu.ErrUnsupported, r.gl.Renderer(), b.Format, b.Modifier)
	}
	img, err := r.d.ImportDMABUF(b)
	if err != nil {
		return nil, err
	}
	if err := r.enter(); err != nil {
		return nil, errors.Join(err, img.Destroy())
	}
	t := &texture{r: r, size: b.Size, target: gpu.Texture2D, img: img}
	if external {
		t.target = gpu.TextureExternalOES
	}
	r.gl.GenTextures(1, &t.id)
	r.gl.BindTexture(t.target, t.id)
	r.gl.TexParameteri(t.target, gpu.TextureWrapS, gpu.ClampToEdge)
	r.gl.TexParameteri(t.target, gpu.TextureWrapT, gpu.ClampToEdge)
	r.gl.EGLImageTargetTexture2D(t.target, img)
	if err := r.leave("gles: import a dmabuf texture"); err != nil {
		return nil, errors.Join(err, t.Destroy())
	}
	return t, nil
}

func (t *texture) Update(stride int, data []byte, damage render.Region) error {
	if t.id == 0 {
		return errors.New("gles: update of a destroyed texture")
	}
	if t.img != nil {
		return errors.New("gles: a dmabuf texture is updated by its client, not uploaded")
	}
	if err := fits(t.size, stride, data); err != nil {
		return err
	}
	gl := t.r.gl
	if err := t.r.enter(); err != nil {
		return err
	}
	gl.BindTexture(gpu.Texture2D, t.id)
	gl.PixelStorei(gpu.UnpackRowLengthExt, int32(stride/4)) //nolint:gosec // bounded by fits
	bounds := image.Rectangle{Max: t.size}
	for _, d := range damage {
		d = d.Intersect(bounds)
		if d.Empty() {
			continue
		}
		gl.PixelStorei(gpu.UnpackSkipPixelsExt, int32(d.Min.X))                                                          //nolint:gosec // inside the texture
		gl.PixelStorei(gpu.UnpackSkipRowsExt, int32(d.Min.Y))                                                            //nolint:gosec // inside the texture
		gl.TexSubImage2D(gpu.Texture2D, int32(d.Min.X), int32(d.Min.Y), int32(d.Dx()), int32(d.Dy()), gpu.BGRAExt, data) //nolint:gosec // inside the texture
	}
	gl.PixelStorei(gpu.UnpackRowLengthExt, 0)
	gl.PixelStorei(gpu.UnpackSkipPixelsExt, 0)
	gl.PixelStorei(gpu.UnpackSkipRowsExt, 0)
	return t.r.leave("gles: update texture")
}

func (t *texture) Destroy() error {
	if t.id == 0 {
		return nil
	}
	if err := t.r.enter(); err != nil {
		return err
	}
	t.r.gl.DeleteTextures(1, &t.id)
	t.id = 0
	err := t.r.leave("gles: destroy texture")
	if t.img != nil {
		err = errors.Join(err, t.img.Destroy())
		t.img = nil
	}
	return err
}

type Framebuffer struct {
	r    *Renderer
	size image.Point
	fbo  uint32
	tex  uint32
	rb   uint32
	img  *gpu.Image
}

func (r *Renderer) NewFramebuffer(size image.Point) (*Framebuffer, error) {
	if size.X <= 0 || size.Y <= 0 {
		return nil, fmt.Errorf("gles: cannot make a %v framebuffer", size)
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	f := &Framebuffer{r: r, size: size}
	r.gl.GenTextures(1, &f.tex)
	r.gl.BindTexture(gpu.Texture2D, f.tex)
	r.gl.TexImage2D(gpu.Texture2D, gpu.RGBA, int32(size.X), int32(size.Y), gpu.RGBA, nil) //nolint:gosec // framebuffer sizes fit GLsizei
	r.gl.GenFramebuffers(1, &f.fbo)
	r.gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
	r.gl.FramebufferTexture2D(gpu.Framebuffer, gpu.ColorAttachment0, gpu.Texture2D, f.tex, 0)
	return f, f.complete()
}

func (r *Renderer) ImportFramebuffer(b gpu.DMABUF) (*Framebuffer, error) {
	img, err := r.d.ImportDMABUF(b)
	if err != nil {
		return nil, err
	}
	if err := r.enter(); err != nil {
		return nil, errors.Join(err, img.Destroy())
	}
	f := &Framebuffer{r: r, size: b.Size, img: img}
	r.gl.GenRenderbuffers(1, &f.rb)
	r.gl.BindRenderbuffer(gpu.Renderbuffer, f.rb)
	r.gl.EGLImageTargetRenderbuffer(img)
	r.gl.GenFramebuffers(1, &f.fbo)
	r.gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
	r.gl.FramebufferRenderbuffer(gpu.Framebuffer, gpu.ColorAttachment0, gpu.Renderbuffer, f.rb)
	return f, f.complete()
}

func (f *Framebuffer) complete() error {
	status := f.r.gl.CheckFramebufferStatus(gpu.Framebuffer)
	f.r.gl.BindFramebuffer(gpu.Framebuffer, 0)
	err := f.r.leave("gles: make a framebuffer")
	if status != gpu.FramebufferComplete {
		err = errors.Join(fmt.Errorf("gles: %v framebuffer is incomplete: %#x", f.size, status), err)
	}
	if err != nil {
		return errors.Join(err, f.Destroy())
	}
	return nil
}

func (f *Framebuffer) Size() image.Point {
	return f.size
}

func (f *Framebuffer) Read(pix []byte, stride int) error {
	if stride < f.size.X*4 || len(pix) < stride*(f.size.Y-1)+f.size.X*4 {
		return fmt.Errorf("gles: reading %v needs stride %d and %d bytes", f.size, f.size.X*4, f.size.X*4*f.size.Y)
	}
	if err := f.r.enter(); err != nil {
		return err
	}
	gl := f.r.gl
	gl.BindFramebuffer(gpu.Framebuffer, f.fbo)
	gl.PixelStorei(gpu.PackAlignment, 4)
	row := make([]byte, f.size.X*4)
	for y := range f.size.Y {
		gl.ReadPixels(0, int32(y), int32(f.size.X), 1, gpu.RGBA, row) //nolint:gosec // inside the framebuffer
		out := pix[y*stride : y*stride+len(row)]
		for x := 0; x < len(row); x += 4 {
			out[x], out[x+1], out[x+2], out[x+3] = row[x+2], row[x+1], row[x], row[x+3]
		}
	}
	gl.BindFramebuffer(gpu.Framebuffer, 0)
	return f.r.leave("gles: read a framebuffer")
}

func (f *Framebuffer) Destroy() error {
	if f.fbo == 0 && f.tex == 0 && f.rb == 0 && f.img == nil {
		return nil
	}
	if err := f.r.enter(); err != nil {
		return err
	}
	gl := f.r.gl
	gl.DeleteFramebuffers(1, &f.fbo)
	gl.DeleteTextures(1, &f.tex)
	gl.DeleteRenderbuffers(1, &f.rb)
	f.fbo, f.tex, f.rb = 0, 0, 0
	err := f.r.leave("gles: destroy a framebuffer")
	if f.img != nil {
		err = errors.Join(err, f.img.Destroy())
		f.img = nil
	}
	return err
}

type pass struct {
	owner  *Renderer
	active bool
	fb     *Framebuffer
	damage []image.Rectangle
	err    error
}

func (r *Renderer) Begin(target render.Target, damage render.Region) (render.Pass, error) {
	fb, ok := target.(*Framebuffer)
	switch {
	case !ok:
		return nil, fmt.Errorf("gles: cannot render into %T", target)
	case fb.r != r || fb.fbo == 0:
		return nil, errors.New("gles: the framebuffer belongs to another renderer or was destroyed")
	case r.pass.active:
		return nil, errors.New("gles: Begin while a pass is active")
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	p := &r.pass
	p.active, p.fb, p.err = true, fb, nil
	p.damage = p.damage[:0]
	bounds := image.Rectangle{Max: fb.size}
	for _, d := range damage {
		if d = d.Intersect(bounds); !d.Empty() {
			p.damage = append(p.damage, d)
		}
	}
	gl := r.gl
	gl.BindFramebuffer(gpu.Framebuffer, fb.fbo)
	gl.Viewport(0, 0, int32(fb.size.X), int32(fb.size.Y)) //nolint:gosec // framebuffer sizes fit GLsizei
	gl.Enable(gpu.Blend)
	gl.BlendFunc(gpu.One, gpu.OneMinusSrcAlpha)
	gl.Enable(gpu.ScissorTest)
	gl.ActiveTexture(gpu.Texture0)
	gl.BindBuffer(gpu.ArrayBuffer, r.vbo)
	return p, nil
}

func (p *pass) fail(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *pass) End() error {
	if !p.active {
		return errors.New("gles: End without an active pass")
	}
	gl := p.owner.gl
	gl.Disable(gpu.ScissorTest)
	gl.Disable(gpu.Blend)
	gl.BindFramebuffer(gpu.Framebuffer, 0)
	gl.Flush()
	p.active, p.fb = false, nil
	return errors.Join(p.err, p.owner.leave("gles: render pass"))
}

func (p *pass) draw(prog program, dst image.Rectangle) {
	gl := p.owner.gl
	v := &p.owner.verts
	x0, y0, x1, y1 := float32(dst.Min.X), float32(dst.Min.Y), float32(dst.Max.X), float32(dst.Max.Y)
	v[0], v[1], v[4], v[5], v[8], v[9], v[12], v[13] = x0, y0, x1, y0, x0, y1, x1, y1
	gl.UseProgram(prog.id)
	gl.Uniform2f(prog.size, float32(p.fb.size.X), float32(p.fb.size.Y))
	gl.BufferData(gpu.ArrayBuffer, v[:], gpu.StreamDraw)
	for _, a := range [2]struct {
		loc    int32
		offset uintptr
	}{{prog.pos, 0}, {prog.texcoord, 8}} {
		if a.loc >= 0 {
			gl.VertexAttribPointer(uint32(a.loc), 2, gpu.Float, 0, 16, a.offset)
			gl.EnableVertexAttribArray(uint32(a.loc))
		}
	}
	for _, d := range p.damage {
		clip := dst.Intersect(d)
		if clip.Empty() {
			continue
		}
		gl.Scissor(int32(clip.Min.X), int32(clip.Min.Y), int32(clip.Dx()), int32(clip.Dy())) //nolint:gosec // inside the framebuffer
		gl.DrawArrays(gpu.TriangleStrip, 0, 4)
	}
}

func (p *pass) FillRect(r image.Rectangle, c render.Color) {
	switch {
	case !p.active:
		p.fail(errors.New("gles: FillRect outside a pass"))
		return
	case !c.Premultiplied():
		p.fail(fmt.Errorf("gles: color %+v is not premultiplied", c))
		return
	case r.Empty():
		return
	}
	prog := p.owner.solid
	p.owner.gl.UseProgram(prog.id)
	p.owner.gl.Uniform4f(prog.color, float32(c.R)/255, float32(c.G)/255, float32(c.B)/255, float32(c.A)/255)
	p.draw(prog, r)
}

func (p *pass) DrawTexture(t render.Texture, src render.FRect, dst image.Rectangle, transform render.Transform, alpha float32) {
	tex, ok := t.(*texture)
	switch {
	case !p.active:
		p.fail(errors.New("gles: DrawTexture outside a pass"))
		return
	case !ok || tex.r != p.owner:
		p.fail(fmt.Errorf("gles: texture %T belongs to another renderer", t))
		return
	case tex.id == 0:
		p.fail(errors.New("gles: texture used after Destroy"))
		return
	case dst.Empty():
		return
	}
	if err := render.CheckDraw(tex.size, src, transform, alpha); err != nil {
		p.fail(err)
		return
	}
	if alpha == 0 {
		return
	}
	prog := p.owner.rgba
	switch {
	case tex.target == gpu.TextureExternalOES:
		prog = p.owner.external
	case tex.opaque:
		prog = p.owner.rgbx
	}
	gl := p.owner.gl
	gl.UseProgram(prog.id)
	gl.Uniform1f(prog.alpha, alpha)
	gl.BindTexture(tex.target, tex.id)
	filter := int32(gpu.Linear)
	if transform.Size(image.Pt(int(src.W), int(src.H))) == dst.Size() && src.W == float64(int(src.W)) && src.H == float64(int(src.H)) {
		filter = gpu.Nearest
	}
	gl.TexParameteri(tex.target, gpu.TextureMinFilter, filter)
	gl.TexParameteri(tex.target, gpu.TextureMagFilter, filter)
	h, v := transform.Axes()
	verts := &p.owner.verts
	for i, corner := range [4][2]float64{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		var s [2]float64
		s[h.Source] = flip(corner[0], h.Reversed)
		s[v.Source] = flip(corner[1], v.Reversed)
		verts[i*4+2] = float32((src.X + s[0]*src.W) / float64(tex.size.X))
		verts[i*4+3] = float32((src.Y + s[1]*src.H) / float64(tex.size.Y))
	}
	p.draw(prog, dst)
}

func flip(f float64, reversed bool) float64 {
	if reversed {
		return 1 - f
	}
	return f
}
