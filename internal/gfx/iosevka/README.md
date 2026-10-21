# Iosevka Term (subset)

neonmux embeds [Iosevka](https://github.com/be5invis/Iosevka) by Renzhi Li
(Belleve Invis) as its default terminal font. It is licensed under the SIL
Open Font License 1.1, see [LICENSE.md](LICENSE.md). The fonts in this
directory stay under that license: the MIT license of neonmux does not apply
to them.

These files are a **modified version** of Iosevka Term 34.9.0
(`PkgTTF-IosevkaTerm-34.9.0.zip` from the official releases). The full fonts
weigh about 11 MB per style. To keep the repository and the binary small,
they were cut down to the scripts and symbols a terminal shows most often,
without hinting instructions (Go's font rasterizer does not run them) and
without OpenType layout features (neonmux draws no ligatures):

- Latin (Basic, Latin-1, Extended-A/B and Additional), IPA, Greek, Cyrillic
- punctuation, super/subscripts, currency, letterlike symbols, number forms
- arrows, mathematical operators, technical symbols, control pictures,
  enclosed alphanumerics
- box drawing, block elements, geometric shapes, miscellaneous symbols,
  dingbats, Braille patterns
- Powerline symbols

Characters outside this set fall back to the built-in pixel font.

## Regenerating

With [fontTools](https://github.com/fonttools/fonttools) installed and the
official TTF package unzipped in the current directory:

```sh
for s in Regular Bold Italic BoldItalic; do
  pyftsubset "IosevkaTerm-$s.ttf" \
    --unicodes='U+0020-007E,U+00A0-02FF,U+0370-03FF,U+0400-04FF,U+1E00-1EFF,U+2000-23FF,U+2400-243F,U+2460-24FF,U+2500-27BF,U+27C0-27FF,U+2800-28FF,U+2900-29FF,U+2B00-2BFF,U+E0A0-E0D7,U+FFFD' \
    --layout-features='' --no-hinting --name-IDs='*' --name-languages='*' \
    --output-file="path/to/neonmux/internal/gfx/iosevka/IosevkaTerm-$s.ttf"
done
```
