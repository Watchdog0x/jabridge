package main

func (f *frame) withoutClip() func() { old := f.clip; f.clip = false; return func() { f.clip = old } }
