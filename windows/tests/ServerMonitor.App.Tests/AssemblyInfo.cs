// Serial, deliberately. Two things here are process-global: the one STA
// dispatcher every render goes through, and Palette's light/dark flag, which
// the theme test flips. Run in parallel, a theme test would recolour a chart
// mid-render in another class.
[assembly: Xunit.CollectionBehavior(DisableTestParallelization = true)]
