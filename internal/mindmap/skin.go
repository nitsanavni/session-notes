package mindmap

// Skin defines how connectors are drawn. Junction runes are keyed by which arms
// meet at a cell (up|down|left|right bitmask). Phase 1 ships only the rounded
// skin; mm's sharp/heavy/double/ascii skins are a later nicety.
type Skin struct {
	Name     string
	Dash     rune
	Junction map[int]rune
}

// makeSkin builds a skin from the arm-combination glyphs, in mm's fixed order:
// up|down, up|left, up|right, down|left, down|right, up|down|left,
// up|down|right, left|right, all, up|left|right, down|left|right.
func makeSkin(name string, dash rune, chars []rune) Skin {
	v, ul, ur, dl, dr, vdl, vdr, h, x, hu, hd :=
		chars[0], chars[1], chars[2], chars[3], chars[4],
		chars[5], chars[6], chars[7], chars[8], chars[9], chars[10]
	return Skin{
		Name: name,
		Dash: dash,
		Junction: map[int]rune{
			up | down:                v,
			up | left:                ul,
			up | right:               ur,
			down | left:              dl,
			down | right:             dr,
			up | down | left:         vdl,
			up | down | right:        vdr,
			left | right:             h,
			up | down | left | right: x,
			up | left | right:        hu,
			down | left | right:      hd,
		},
	}
}

// Rounded is the default (and, in phase 1, only) skin.
var Rounded = makeSkin("rounded", '─', []rune{'│', '╯', '╰', '╮', '╭', '┤', '├', '─', '┼', '┴', '┬'})
