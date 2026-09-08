using System.Windows.Automation;
using System.Windows.Controls;
using ServerMonitor.App.Controls;
using ServerMonitor.Core.L10n;
using Xunit;

namespace ServerMonitor.App.Tests;

/// <summary>
/// A labelled row, as assistive technology sees it.
/// </summary>
/// <remarks>
/// Ui.Field puts a label in one grid column and its control in the next,
/// which is enough for someone reading the screen and nothing at all for
/// someone who is not: adjacency means nothing to the automation tree. A
/// walk of the live tree found unnamed controls on every page — seven combo
/// boxes on the settings page alone, and a row of unnamed text fields in the
/// server editor.
///
/// LabeledBy rather than a copied Name, so the two cannot drift and a runtime
/// language switch renames both at once.
/// </remarks>
public class FieldTests
{
    [Fact]
    public void TheLabelNamesTheControl()
    {
        var box = UiThread.Run(() => Ui.Input("22", 70));
        UiThread.Run(() => Ui.Field("server.port", box));

        var labelledBy = UiThread.Run(() => AutomationProperties.GetLabeledBy(box));
        Assert.NotNull(labelledBy);
        Assert.Equal(
            Strings.Get("server.port"),
            UiThread.Run(() => ((TextBlock)labelledBy).Text));
    }

    [Fact]
    public void AFieldFromLiteralTextLabelsItTheSameWay()
    {
        // The Windows-only rows — the SSH transport, the data paths — go
        // through FieldText, which had the same gap.
        var picker = UiThread.Run(() => Ui.Picker(
            new[] { "a", "b" }, "a", value => value, _ => { }));
        UiThread.Run(() => Ui.FieldText("SSH transport", picker));

        var labelledBy = UiThread.Run(() => AutomationProperties.GetLabeledBy(picker));
        Assert.NotNull(labelledBy);
        Assert.Equal("SSH transport", UiThread.Run(() => ((TextBlock)labelledBy).Text));
    }

    [Fact]
    public void AHelpLineDoesNotBecomeTheName()
    {
        // The help text sits under the control in the same cell, so the naive
        // fix — name the control after whatever is nearest — would have
        // announced the explanation instead of the label.
        var box = UiThread.Run(() => Ui.Input());
        UiThread.Run(() => Ui.Field("server.country", box, "server.countryHelp"));

        var labelledBy = UiThread.Run(() => AutomationProperties.GetLabeledBy(box));
        Assert.Equal(
            Strings.Get("server.country"),
            UiThread.Run(() => ((TextBlock)labelledBy!).Text));
    }

    [Fact]
    public void TheKeyboardHelpersMarkTheRightButtons()
    {
        // None of the editors set these, so Enter did nothing in any of them
        // and Escape did not close them — verified in the running app before
        // and after. WPF handles the rest once the flags are on.
        var save = UiThread.Run(() => Ui.Default(Ui.Accent("Save", () => { })));
        var cancel = UiThread.Run(() => Ui.Cancels(Ui.Button("Cancel", () => { })));

        Assert.True(UiThread.Run(() => save.IsDefault), "Enter does not press Save");
        Assert.True(UiThread.Run(() => cancel.IsCancel), "Escape does not press Cancel");

        // And they are not the same flag: a button that is both would fire on
        // either key, which for a Save is how you lose an edit to a stray
        // Escape.
        Assert.False(UiThread.Run(() => save.IsCancel), "Save also answers Escape");
        Assert.False(UiThread.Run(() => cancel.IsDefault), "Cancel also answers Enter");
    }
}
