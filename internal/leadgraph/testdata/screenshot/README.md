# Screenshot import fixtures

A **synthetic** mind map and readings of it recorded from the live vision model.
Every name, company and number here is invented. The structure copies the
quirks of the product owner's real screenshot (2026-09-11), which is not in this
repository because it holds real people's names, ages and salaries.

| File | What it is |
|---|---|
| `truth.json` | The answer key: every topic, its parent, its marks, and what it really is (company, person, note, placeholder), plus each person's name and title and whether they have left |
| `mindmap.html` | The picture's source, generated from `truth.json` |
| `mindmap.png` | The picture: `mindmap.html` rendered at 1x, 1500x860 |
| `prompt.txt` | The exact prompt the readings were produced with. Must equal `leadgraph.TranscriptPrompt` |
| `reading-qwen3.7-plus.json` | A correct reading: 31 topics, every parent and name right. Two runs returned byte-identical text |
| `reading-qwen3.8-flash-split.json` | A faulty reading from qwen3.8-flash. It split the yellow-highlighted stretch into its own topic (32 topics), which hangs two people under the wrong parent and leaves one person without a name. This happened on 2 of 4 runs. The planner is tested against it so that a bad reading surfaces as rows to correct instead of being quietly smoothed over |

The readings are the model's text verbatim, not edited.

## The quirks the map carries, and why each matters

- A leading number is an age on some lines ("41 女 沈若溪") and not on others
  ("8 秦朗", "12 创意设计主管"). It is kept verbatim and never parsed.
- The name sits in different places ("林默然，设计总监…" and "39 创新设计部 负责人 程昱…"),
  and some names are English ("Leo Chen", "Creative Team Leader Marcus").
- A child of a person is usually their report but not always ("韩子墨 → qmx 品牌").
- Two people have left ("已离职去Brightly", "24年离开").
- A placeholder topic ("输入文本").
- Whole-topic fills, inline highlights ("**34** 周予安 … **海硕**"), a yellow
  highlight on part of a line, orange bold text, and a green connector.

## Re-rendering the picture

```bash
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --disable-gpu \
  --hide-scrollbars --force-device-scale-factor=1 --window-size=1500,860 \
  --default-background-color=ffffffff --virtual-time-budget=3000 \
  --screenshot=mindmap.png "file://$PWD/mindmap.html"
```

If you change `truth.json`, change `mindmap.html` to match.
`TestThePictureAndItsAnswerKeyDescribeTheSameMap` fails when they disagree.

## Re-recording a reading

The prompt and the readings move together. After changing `leadgraph.TranscriptPrompt`:

```bash
make vision-record MODEL=qwen3.7-plus
```

That reads `mindmap.png` through the production path (`vision.Reader`, the same
two calls, the same image-token check) and writes `reading-<model>.json`. Then
copy the new prompt into `prompt.txt`. `TestTheRecordedReadingsCameFromThisPrompt`
fails until you do.
