package gles

import (
	"errors"

	"github.com/stubbedev/tilde/internal/gpu"
)

type Stack struct {
	gbmLib   *gpu.GBMLibrary
	eglLib   *gpu.Library
	GBM      *gpu.GBM
	Display  *gpu.Display
	Renderer *Renderer
}

func OnGBM(fd int, paths gpu.Paths) (*Stack, error) {
	s := &Stack{}
	if err := s.open(fd, paths); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

func (s *Stack) open(fd int, paths gpu.Paths) error {
	var err error
	if s.gbmLib, err = gpu.LoadGBM(paths); err != nil {
		return err
	}
	if s.GBM, err = s.gbmLib.Open(fd); err != nil {
		return err
	}
	if s.eglLib, err = gpu.Load(paths); err != nil {
		return err
	}
	if s.Display, err = s.eglLib.OnGBM(s.GBM); err != nil {
		return err
	}
	s.Renderer, err = New(s.Display)
	return err
}

func (s *Stack) Close() error {
	var errs []error
	if s.Renderer != nil {
		errs = append(errs, s.Renderer.Close())
	}
	if s.Display != nil {
		errs = append(errs, s.Display.Close())
	}
	if s.GBM != nil {
		s.GBM.Close()
	}
	if s.gbmLib != nil {
		errs = append(errs, s.gbmLib.Close())
	}
	if s.eglLib != nil {
		errs = append(errs, s.eglLib.Close())
	}
	return errors.Join(errs...)
}
