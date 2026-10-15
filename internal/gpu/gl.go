package gpu

import (
	"errors"
	"fmt"
	"runtime"
)

const (
	NoError                       = 0
	Texture2D                     = 0x0DE1
	TextureExternalOES            = 0x8D65
	Texture0                      = 0x84C0
	TextureMinFilter              = 0x2801
	TextureMagFilter              = 0x2800
	TextureWrapS                  = 0x2802
	TextureWrapT                  = 0x2803
	Linear                        = 0x2601
	Nearest                       = 0x2600
	ClampToEdge                   = 0x812F
	RGBA                          = 0x1908
	BGRAExt                       = 0x80E1
	RGBA8OES                      = 0x8058
	UnsignedByte                  = 0x1401
	Float                         = 0x1406
	UnpackAlignment               = 0x0CF5
	PackRowLengthNV               = 0x0D02
	UnpackRowLengthExt            = 0x0CF2
	UnpackSkipPixelsExt           = 0x0CF4
	UnpackSkipRowsExt             = 0x0CF3
	Framebuffer                   = 0x8D40
	Renderbuffer                  = 0x8D41
	ColorAttachment0              = 0x8CE0
	FramebufferComplete           = 0x8CD5
	Blend                         = 0x0BE2
	ScissorTest                   = 0x0C11
	Zero                          = 0
	One                           = 1
	OneMinusSrcAlpha              = 0x0303
	ColorBufferBit                = 0x4000
	VertexShader                  = 0x8B31
	FragmentShader                = 0x8B30
	CompileStatus                 = 0x8B81
	LinkStatus                    = 0x8B82
	InfoLogLength                 = 0x8B84
	TriangleStrip                 = 0x0005
	ArrayBuffer                   = 0x8892
	StaticDraw                    = 0x88E4
	StreamDraw                    = 0x88E0
	glVendor                      = 0x1F00
	glRenderer                    = 0x1F01
	glVersion                     = 0x1F02
	glExtensions                  = 0x1F03
	glInvalidEnum                 = 0x0500
	glInvalidValue                = 0x0501
	glInvalidOperation            = 0x0502
	glOutOfMemory                 = 0x0505
	glInvalidFramebufferOperation = 0x0506
)

var glErrorNames = map[uint32]string{
	glInvalidEnum: "GL_INVALID_ENUM", glInvalidValue: "GL_INVALID_VALUE", glInvalidOperation: "GL_INVALID_OPERATION",
	glOutOfMemory: "GL_OUT_OF_MEMORY", glInvalidFramebufferOperation: "GL_INVALID_FRAMEBUFFER_OPERATION",
}

type GLError struct {
	Op   string
	Code uint32
}

func (e *GLError) Error() string {
	name, ok := glErrorNames[e.Code]
	if !ok {
		name = fmt.Sprintf("0x%x", e.Code)
	}
	return fmt.Sprintf("gpu: %s: %s", e.Op, name)
}

type GL struct {
	getError                 func() uint32
	getString                func(name uint32) string
	GenTextures              func(n int32, textures *uint32)
	DeleteTextures           func(n int32, textures *uint32)
	BindTexture              func(target, texture uint32)
	ActiveTexture            func(texture uint32)
	TexParameteri            func(target, pname uint32, param int32)
	texImage2D               func(target uint32, level, internalFormat, width, height, border int32, format, typ uint32, pixels *byte)
	texSubImage2D            func(target uint32, level, x, y, width, height int32, format, typ uint32, pixels *byte)
	PixelStorei              func(pname uint32, param int32)
	GenFramebuffers          func(n int32, fbs *uint32)
	DeleteFramebuffers       func(n int32, fbs *uint32)
	BindFramebuffer          func(target, fb uint32)
	FramebufferTexture2D     func(target, attachment, texTarget, texture uint32, level int32)
	FramebufferRenderbuffer  func(target, attachment, rbTarget, rb uint32)
	CheckFramebufferStatus   func(target uint32) uint32
	GenRenderbuffers         func(n int32, rbs *uint32)
	DeleteRenderbuffers      func(n int32, rbs *uint32)
	BindRenderbuffer         func(target, rb uint32)
	RenderbufferStorage      func(target, internalFormat uint32, width, height int32)
	GenBuffers               func(n int32, buffers *uint32)
	DeleteBuffers            func(n int32, buffers *uint32)
	BindBuffer               func(target, buffer uint32)
	bufferData               func(target uint32, size int, data *float32, usage uint32)
	Viewport                 func(x, y, width, height int32)
	Scissor                  func(x, y, width, height int32)
	Enable                   func(capability uint32)
	Disable                  func(capability uint32)
	BlendFunc                func(src, dst uint32)
	ClearColor               func(r, g, b, a float32)
	Clear                    func(mask uint32)
	CreateShader             func(typ uint32) uint32
	shaderSource             func(shader uint32, count int32, sources **byte, lengths *int32)
	CompileShader            func(shader uint32)
	GetShaderiv              func(shader, pname uint32, params *int32)
	getShaderInfoLog         func(shader uint32, size int32, length *int32, log *byte)
	DeleteShader             func(shader uint32)
	CreateProgram            func() uint32
	AttachShader             func(program, shader uint32)
	LinkProgram              func(program uint32)
	GetProgramiv             func(program, pname uint32, params *int32)
	getProgramInfoLog        func(program uint32, size int32, length *int32, log *byte)
	UseProgram               func(program uint32)
	DeleteProgram            func(program uint32)
	GetUniformLocation       func(program uint32, name string) int32
	GetAttribLocation        func(program uint32, name string) int32
	Uniform1i                func(location, v int32)
	Uniform1f                func(location int32, v float32)
	Uniform2f                func(location int32, a, b float32)
	Uniform4f                func(location int32, a, b, c, d float32)
	uniformMatrix3fv         func(location, count int32, transpose uint8, value *float32)
	VertexAttribPointer      func(index uint32, size int32, typ uint32, normalized uint8, stride int32, offset uintptr)
	EnableVertexAttribArray  func(index uint32)
	DisableVertexAttribArray func(index uint32)
	DrawArrays               func(mode uint32, first, count int32)
	readPixels               func(x, y, width, height int32, format, typ uint32, pixels *byte)
	Flush                    func()
	Finish                   func()
	imageTargetTexture2D     func(target uint32, image uintptr)
	imageTargetRenderbuffer  func(target uint32, image uintptr)
}

func (gl *GL) load(l *Library, path string) error {
	if err := bind(l.glesLib, path, []symbol{
		{&gl.getError, "glGetError"},
		{&gl.getString, "glGetString"},
		{&gl.GenTextures, "glGenTextures"},
		{&gl.DeleteTextures, "glDeleteTextures"},
		{&gl.BindTexture, "glBindTexture"},
		{&gl.ActiveTexture, "glActiveTexture"},
		{&gl.TexParameteri, "glTexParameteri"},
		{&gl.texImage2D, "glTexImage2D"},
		{&gl.texSubImage2D, "glTexSubImage2D"},
		{&gl.PixelStorei, "glPixelStorei"},
		{&gl.GenFramebuffers, "glGenFramebuffers"},
		{&gl.DeleteFramebuffers, "glDeleteFramebuffers"},
		{&gl.BindFramebuffer, "glBindFramebuffer"},
		{&gl.FramebufferTexture2D, "glFramebufferTexture2D"},
		{&gl.FramebufferRenderbuffer, "glFramebufferRenderbuffer"},
		{&gl.CheckFramebufferStatus, "glCheckFramebufferStatus"},
		{&gl.GenRenderbuffers, "glGenRenderbuffers"},
		{&gl.DeleteRenderbuffers, "glDeleteRenderbuffers"},
		{&gl.BindRenderbuffer, "glBindRenderbuffer"},
		{&gl.RenderbufferStorage, "glRenderbufferStorage"},
		{&gl.GenBuffers, "glGenBuffers"},
		{&gl.DeleteBuffers, "glDeleteBuffers"},
		{&gl.BindBuffer, "glBindBuffer"},
		{&gl.bufferData, "glBufferData"},
		{&gl.Viewport, "glViewport"},
		{&gl.Scissor, "glScissor"},
		{&gl.Enable, "glEnable"},
		{&gl.Disable, "glDisable"},
		{&gl.BlendFunc, "glBlendFunc"},
		{&gl.ClearColor, "glClearColor"},
		{&gl.Clear, "glClear"},
		{&gl.CreateShader, "glCreateShader"},
		{&gl.shaderSource, "glShaderSource"},
		{&gl.CompileShader, "glCompileShader"},
		{&gl.GetShaderiv, "glGetShaderiv"},
		{&gl.getShaderInfoLog, "glGetShaderInfoLog"},
		{&gl.DeleteShader, "glDeleteShader"},
		{&gl.CreateProgram, "glCreateProgram"},
		{&gl.AttachShader, "glAttachShader"},
		{&gl.LinkProgram, "glLinkProgram"},
		{&gl.GetProgramiv, "glGetProgramiv"},
		{&gl.getProgramInfoLog, "glGetProgramInfoLog"},
		{&gl.UseProgram, "glUseProgram"},
		{&gl.DeleteProgram, "glDeleteProgram"},
		{&gl.GetUniformLocation, "glGetUniformLocation"},
		{&gl.GetAttribLocation, "glGetAttribLocation"},
		{&gl.Uniform1i, "glUniform1i"},
		{&gl.Uniform1f, "glUniform1f"},
		{&gl.Uniform2f, "glUniform2f"},
		{&gl.Uniform4f, "glUniform4f"},
		{&gl.uniformMatrix3fv, "glUniformMatrix3fv"},
		{&gl.VertexAttribPointer, "glVertexAttribPointer"},
		{&gl.EnableVertexAttribArray, "glEnableVertexAttribArray"},
		{&gl.DisableVertexAttribArray, "glDisableVertexAttribArray"},
		{&gl.DrawArrays, "glDrawArrays"},
		{&gl.readPixels, "glReadPixels"},
		{&gl.Flush, "glFlush"},
		{&gl.Finish, "glFinish"},
	}); err != nil {
		return err
	}
	return errors.Join(
		l.proc(symbol{&gl.imageTargetTexture2D, "glEGLImageTargetTexture2DOES"}),
		l.proc(symbol{&gl.imageTargetRenderbuffer, "glEGLImageTargetRenderbufferStorageOES"}),
	)
}

func (gl *GL) Check(op string) error {
	if code := gl.getError(); code != NoError {
		for gl.getError() != NoError {
		}
		return &GLError{Op: op, Code: code}
	}
	return nil
}

func (gl *GL) Vendor() string     { return gl.getString(glVendor) }
func (gl *GL) Renderer() string   { return gl.getString(glRenderer) }
func (gl *GL) Version() string    { return gl.getString(glVersion) }
func (gl *GL) Extensions() string { return gl.getString(glExtensions) }

func (gl *GL) TexImage2D(target uint32, internalFormat int32, width, height int32, format uint32, pixels []byte) {
	gl.texImage2D(target, 0, internalFormat, width, height, 0, format, UnsignedByte, first(pixels))
	runtime.KeepAlive(pixels)
}

func (gl *GL) TexSubImage2D(target uint32, x, y, width, height int32, format uint32, pixels []byte) {
	gl.texSubImage2D(target, 0, x, y, width, height, format, UnsignedByte, first(pixels))
	runtime.KeepAlive(pixels)
}

func (gl *GL) ReadPixels(x, y, width, height int32, format uint32, pixels []byte) {
	gl.readPixels(x, y, width, height, format, UnsignedByte, first(pixels))
	runtime.KeepAlive(pixels)
}

func (gl *GL) BufferData(target uint32, values []float32, usage uint32) {
	gl.bufferData(target, len(values)*4, first(values), usage)
	runtime.KeepAlive(values)
}

func (gl *GL) UniformMatrix3(location int32, m *[9]float32) {
	gl.uniformMatrix3fv(location, 1, 0, &m[0])
	runtime.KeepAlive(m)
}

func (gl *GL) EGLImageTargetTexture2D(target uint32, image *Image) {
	gl.imageTargetTexture2D(target, image.handle())
}

func (gl *GL) EGLImageTargetRenderbuffer(image *Image) {
	gl.imageTargetRenderbuffer(Renderbuffer, image.handle())
}

func first[T any](s []T) *T {
	if len(s) == 0 {
		return nil
	}
	return &s[0]
}

func (gl *GL) Compile(typ uint32, source string) (uint32, error) {
	shader := gl.CreateShader(typ)
	if shader == 0 {
		return 0, gl.Check("glCreateShader")
	}
	src := append([]byte(source), 0)
	ptr := &src[0]
	gl.shaderSource(shader, 1, &ptr, nil)
	runtime.KeepAlive(src)
	gl.CompileShader(shader)
	var ok int32
	gl.GetShaderiv(shader, CompileStatus, &ok)
	if ok == 0 {
		log := gl.infoLog(shader, gl.GetShaderiv, gl.getShaderInfoLog)
		gl.DeleteShader(shader)
		return 0, fmt.Errorf("gpu: compile shader: %s", log)
	}
	return shader, nil
}

func (gl *GL) Link(vertex, fragment string) (uint32, error) {
	vs, err := gl.Compile(VertexShader, vertex)
	if err != nil {
		return 0, err
	}
	defer gl.DeleteShader(vs)
	fs, err := gl.Compile(FragmentShader, fragment)
	if err != nil {
		return 0, err
	}
	defer gl.DeleteShader(fs)
	program := gl.CreateProgram()
	gl.AttachShader(program, vs)
	gl.AttachShader(program, fs)
	gl.LinkProgram(program)
	var ok int32
	gl.GetProgramiv(program, LinkStatus, &ok)
	if ok == 0 {
		log := gl.infoLog(program, gl.GetProgramiv, gl.getProgramInfoLog)
		gl.DeleteProgram(program)
		return 0, fmt.Errorf("gpu: link program: %s", log)
	}
	return program, nil
}

func (gl *GL) infoLog(object uint32, get func(uint32, uint32, *int32), read func(uint32, int32, *int32, *byte)) string {
	var n int32
	get(object, InfoLogLength, &n)
	if n <= 1 {
		return "no log"
	}
	buf := make([]byte, n)
	read(object, n, &n, &buf[0])
	return string(buf[:n])
}
