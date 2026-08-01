package hashing

// Prose used by the SimHash tests. It is written out rather than loaded from
// testdata because the assertions are about the *relationship* between these
// texts — a word changed here moves a number three files away — and that is
// only reviewable if the texts sit next to the tests that measure them.

// proseOriginal is the baseline document, around 400 words.
const proseOriginal = `
The archive had grown for eleven years without anyone deciding what belonged in
it. Every laptop that was retired arrived as a folder, every phone that filled
up arrived as another, and the folders were named after the month they were
copied rather than anything inside them. Nobody had lied about it and nobody had
planned it either. A backup is easy to make and hard to read, so the pile kept
growing sideways while the question of what it actually contained stayed
politely unasked.

By the time anyone counted, there were four copies of the same holiday, three of
them smaller than the original and one of them rotated a quarter turn. There
were eleven copies of a scanned contract, differing only in which corner the
scanner had clipped. There was a folder called final, a folder called final new,
and a folder called final new use this one, none of which agreed with each
other. This is not a story about carelessness. It is what happens when copying
is cheap and deciding is expensive.

The obvious fix was to open every folder and look. That would have taken a
fortnight of evenings and produced a set of decisions nobody could later explain
or repeat, which is the same as producing no decisions at all. Worse, it would
have been the kind of task that gets abandoned two thirds of the way through,
leaving an archive that is neither sorted nor honestly unsorted.

The useful fix was narrower. Instead of asking what every file was, ask only
which files were the same file wearing a different name, and leave a person to
judge the handful that remained genuinely ambiguous. A machine can answer the
first question honestly and quickly. It cannot answer the second one at all, and
pretending otherwise is how automated tools end up deleting the only surviving
copy of something.

So the tool reports rather than decides. It puts the candidates side by side,
says how confident it is and why, and waits. Nothing is removed permanently, and
nothing is removed without somebody looking at it first. The result is not a
clean archive. It is a smaller pile with a known shape, which is the most that
can be promised and rather more than was there before.
`

// proseEdited is proseOriginal with three words changed: the kind of revision
// that produces a second copy in the first place.
const proseEdited = `
The archive had grown for twelve years without anyone deciding what belonged in
it. Every laptop that was retired arrived as a folder, every phone that filled
up arrived as another, and the folders were named after the month they were
copied rather than anything inside them. Nobody had lied about it and nobody had
planned it either. A backup is easy to make and hard to read, so the pile kept
growing sideways while the question of what it actually contained stayed
quietly unasked.

By the time anyone counted, there were four copies of the same holiday, three of
them smaller than the original and one of them rotated a quarter turn. There
were eleven copies of a scanned contract, differing only in which corner the
scanner had clipped. There was a folder called final, a folder called final new,
and a folder called final new use this one, none of which agreed with each
other. This is not a story about carelessness. It is what happens when copying
is cheap and deciding is expensive.

The obvious fix was to open every folder and look. That would have taken a
month of evenings and produced a set of decisions nobody could later explain
or repeat, which is the same as producing no decisions at all. Worse, it would
have been the kind of task that gets abandoned two thirds of the way through,
leaving an archive that is neither sorted nor honestly unsorted.

The useful fix was narrower. Instead of asking what every file was, ask only
which files were the same file wearing a different name, and leave a person to
judge the handful that remained genuinely ambiguous. A machine can answer the
first question honestly and quickly. It cannot answer the second one at all, and
pretending otherwise is how automated tools end up deleting the only surviving
copy of something.

So the tool reports rather than decides. It puts the candidates side by side,
says how confident it is and why, and waits. Nothing is removed permanently, and
nothing is removed without somebody looking at it first. The result is not a
clean archive. It is a smaller pile with a known shape, which is the most that
can be promised and rather more than was there before.
`

// templateA and templateB are two documents built from one boilerplate with
// different particulars. This is the dominant false-positive class in a real
// documents folder, and the reason the text threshold is 6 rather than the 8
// used for photos: these two must land clearly outside it.
const templateA = `
Dear Ms Okonkwo,

Thank you for your enquiry of the fourth of March regarding the replacement
window units for the rear elevation. I am pleased to enclose our quotation.

We propose to supply and fit six casement units in powder coated aluminium,
finish RAL seven zero one six, glazed with a low emissivity double unit. The
works would take four working days and we would require access to the rear
courtyard throughout. The quoted figure is two thousand eight hundred and forty
pounds including materials, labour and the removal of the existing frames.

This quotation is valid for sixty days. It excludes any making good to internal
plaster, which we would recommend you arrange separately.

Please let me know if you would like to proceed, or if you would prefer us to
revisit any part of the specification.

Yours sincerely,
`

const templateB = `
Dear Mr Halvorsen,

Thank you for your enquiry of the nineteenth of May regarding the replacement
door assembly for the side entrance. I am pleased to enclose our quotation.

We propose to supply and fit two hinged units in powder coated steel, finish
RAL nine zero zero five, glazed with a toughened single unit. The works would
take two working days and we would require access to the side passage
throughout. The quoted figure is one thousand three hundred and ninety pounds
including materials, labour and the removal of the existing frames.

This quotation is valid for sixty days. It excludes any making good to internal
plaster, which we would recommend you arrange separately.

Please let me know if you would like to proceed, or if you would prefer us to
revisit any part of the specification.

Yours sincerely,
`

// proseUnrelated is a different document of similar length and register — the
// realistic negative case, not a random string.
const proseUnrelated = `
Bread is the cheapest thing in the shop and the one most often thrown away. A
loaf costs less than a coffee, keeps for three days if it is any good, and is
discarded on the fourth by people who would not dream of pouring away a bottle
of wine. The reason is not indifference. It is that stale bread announces itself
immediately and unambiguously, while a great many other kinds of waste stay
quietly out of sight in a cupboard.

There is an older set of habits for this, and none of it is complicated. Yesterday's
loaf becomes toast. The day after, it becomes breadcrumbs, which keep for months
in a jar and improve almost anything baked with a crust. Older still, and it
becomes the body of a soup: torn, soaked, and simmered until the distinction
between bread and broth stops being useful. In Tuscany that soup has a name and
a season. In most kitchens it has neither, which is why it does not get made.

What these habits share is that they treat staleness as a change of state rather
than a failure. A loaf does not stop being food on the fourth day; it stops
being one kind of food and starts being another. The recipes are not rescues.
They are the second and third acts of something that was always going to have
three.

The obstacle is not skill and it is not time. Breadcrumbs take four minutes.
The obstacle is that nobody decides on the second day what the loaf is for, and
by the fourth the decision has been made by default. This is the same shape as
most household waste: not a shortage of effort, but a shortage of small
decisions taken early enough to matter.

So the useful advice is not a recipe. It is to buy a smaller loaf, and to decide
on Tuesday what Thursday will do with what is left. Everything after that is
technique, and technique is the easy part.
`
