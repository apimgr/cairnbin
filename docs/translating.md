# Translating Cairnbin

Cairnbin ships eight complete locales: English (`en`), Spanish (`es`), French
(`fr`), German (`de`), Italian (`it`), Chinese (`zh`), Arabic (`ar`), and
Japanese (`ja`). English is the source locale. All locales are embedded in both the server and
CLI binaries.

## Translate a locale

Locale files are in `src/common/i18n/locales/`. Copy `en.json` when adding a
language, then translate every value while preserving the JSON structure,
keys, and key order. Use UTF-8, two-space indentation, and valid JSON. Do not
translate keys, product names, protocol and technology names, code, file
names, configuration keys, URLs, or literal setting values such as `dark` and
`auto`.

Keep every `{placeholder}` exactly as written in English, including its
spelling and braces. Its position may change to fit the translated sentence.
Translate complete sentences rather than assembling fragments. Follow the
language's normal punctuation and typography, and use a consistent, neutral
register.

For pluralized values, provide every category the locale uses. Follow the
locale's CLDR plural rules; do not copy English plural categories blindly.
Arabic pages must remain right-to-left (`meta.direction` is `rtl`).

## Add a new language

1. Add `src/common/i18n/locales/{code}.json` with all keys from `en.json`.
2. Set `meta.language`, `meta.name`, `meta.native_name`, `meta.direction`, and
   `meta.version`. Add `meta.translators` if contributors want credit.
3. Add the language to the canonical registry in `src/common/i18n/i18n.go`,
   including native name, text direction, CLDR plural categories, and whether
   it is part of the required language minimum. Configuration defaults derive
   from this registry.
4. Update this page and the README language list in the same change.
5. Run `make test`; locale tests check key and placeholder consistency.
6. Review the translated interface at 320, 768, and 1280 pixels. Check right-
   to-left layout where applicable and verify long text is not clipped.

The locale file, runtime registry, language selector, CLI language support,
README count, and this page must describe the same supported languages.

## Review and contribution

Anyone may contribute a locale update through a pull request or send the
locale file to the maintainer. A new language includes its locale file and the
registry entry. Updating an existing language changes that locale file while
preserving its complete key set. A fluent speaker must review machine-
generated draft translations before they merge. Reviewers check placeholders,
plural categories, terminology, and the layout sizes above.

**Review status:** Italian (`it`) is a best-effort machine-translated draft;
fluent-speaker review is pending.

When a language's script is not covered by the system font stack, add an
appropriate font fallback and verify long text wraps without spaces. Keep every
locale synchronized with changes to the English source keys; translate added
and changed values and remove deleted keys in the same change.

If an English string is incomplete or ambiguous, report the source key for
clarification instead of working around it in the translation.

## Glossary

| Term | Translation guidance |
|------|----------------------|
| Cairnbin | Keep the product name unchanged. |
| paste | Use one consistent term for a saved text or file snippet. |
| owner token | Preserve the distinction between this paste-owner credential and the server operator token. |
| burn after reads | Describe automatic deletion after the configured number of views. |
| unlisted | A paste reachable by URL but omitted from public listings. |
| token | Keep the credential meaning clear; do not translate literal token values. |

## Translator credits

The locale metadata currently contains no individual translator credits. Add
credits here and to the relevant locale's `meta.translators` when contributors
request attribution.
