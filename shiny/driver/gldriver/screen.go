// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gldriver

import (
	"fmt"
	"image"
	"sync"

	"golang.org/x/exp/shiny/driver/internal/pump"
	"golang.org/x/exp/shiny/screen"
	"golang.org/x/mobile/gl"
)

// glMu is a mutex that enforces the atomicity of methods like Texture.Upload
// or Window.Draw that are conceptually one operation but are implemented by
// multiple OpenGL calls. OpenGL is a stateful API, so interleaving OpenGL
// calls from separate higher-level operations causes inconsistencies.
//
// glMu does not need to be held when accessing gl.WorkAvailable or gl.DoWork.
//
// TODO: is this affected by changing the x/mobile/gl package from an
// (implicit) global context to a per-window context?
var glMu sync.Mutex

var theScreen = &screenImpl{
	windows: make(map[uintptr]*windowImpl),
}

type screenImpl struct {
	texture struct {
		program gl.Program
		pos     gl.Attrib
		mvp     gl.Uniform
		uvp     gl.Uniform
		inUV    gl.Attrib
		sample  gl.Uniform
		quad    gl.Buffer
	}
	fill struct {
		program gl.Program
		pos     gl.Attrib
		mvp     gl.Uniform
		color   gl.Uniform
		quad    gl.Buffer
	}

	mu      sync.Mutex
	windows map[uintptr]*windowImpl
}

func (s *screenImpl) NewBuffer(size image.Point) (retBuf screen.Buffer, retErr error) {
	return &bufferImpl{
		rgba: image.NewRGBA(image.Rectangle{Max: size}),
		size: size,
	}, nil
}

func (s *screenImpl) NewTexture(size image.Point) (screen.Texture, error) {
	glMu.Lock()
	defer glMu.Unlock()

	// TODO: can we compile these programs eagerly instead of lazily?

	// Find a GL context for this texture.
	// TODO: this might be correct. Some GL objects can be shared
	// across contexts. But this needs a review of the spec to make
	// sure it's correct, and some testing would be nice.
	var glctx gl.Context
	for _, w := range s.windows {
		glctx = w.glctx
		break
	}
	if glctx == nil {
		return nil, fmt.Errorf("gldriver: no GL context available")
	}

	if !glctx.IsProgram(s.texture.program) {
		p, err := compileProgram(glctx, textureVertexSrc, textureFragmentSrc)
		if err != nil {
			return nil, err
		}
		s.texture.program = p
		s.texture.pos = glctx.GetAttribLocation(p, "pos")
		s.texture.mvp = glctx.GetUniformLocation(p, "mvp")
		s.texture.uvp = glctx.GetUniformLocation(p, "uvp")
		s.texture.inUV = glctx.GetAttribLocation(p, "inUV")
		s.texture.sample = glctx.GetUniformLocation(p, "sample")
		s.texture.quad = glctx.CreateBuffer()

		glctx.BindBuffer(gl.ARRAY_BUFFER, s.texture.quad)
		glctx.BufferData(gl.ARRAY_BUFFER, quadCoords, gl.STATIC_DRAW)
	}

	t := &textureImpl{
		glctx: glctx,
		id:    glctx.CreateTexture(),
		size:  size,
	}

	glctx.BindTexture(gl.TEXTURE_2D, t.id)
	glctx.TexImage2D(gl.TEXTURE_2D, 0, size.X, size.Y, gl.RGBA, gl.UNSIGNED_BYTE, nil)
	glctx.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	glctx.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	glctx.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	glctx.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)

	return t, nil
}

func (s *screenImpl) NewWindow(opts *screen.NewWindowOptions) (screen.Window, error) {
	// TODO: look at opts.
	const width, height = 1024, 768

	id := newWindow(width, height)
	w := &windowImpl{
		s:        s,
		id:       id,
		pump:     pump.Make(),
		publish:  make(chan struct{}, 1),
		draw:     make(chan struct{}),
		drawDone: make(chan struct{}),
	}

	s.mu.Lock()
	s.windows[id] = w
	s.mu.Unlock()

	w.ctx = showWindow(id)

	go drawLoop(w)

	return w, nil
}
