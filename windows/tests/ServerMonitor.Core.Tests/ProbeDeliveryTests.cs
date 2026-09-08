using ServerMonitor.Core.Collect;
using Xunit;

namespace ServerMonitor.Core.Tests;

/// <summary>
/// What is sent to a Windows host, as opposed to what is in the repository.
/// </summary>
/// <remarks>
/// The script has to fit through cmd.exe's command line, so the delivered
/// form is stripped and deflated. These pin the stripping rules, because
/// getting them wrong produces a script that the host either refuses or —
/// worse — runs differently.
/// </remarks>
public class ProbeDeliveryTests
{
    [Fact]
    public void DeliveryDropsWholeLineCommentsAndBlanks()
    {
        var script = string.Join('\n',
            "# a leading comment",
            "$x = 1",
            "",
            "    # an indented comment",
            "$y = 2",
            "   ",
            "$z = 3");

        Assert.Equal("$x = 1\n$y = 2\n$z = 3", WindowsMetrics.ForDelivery(script));
    }

    [Fact]
    public void DeliveryKeepsATrailingCommentAfterCode()
    {
        // Telling a comment from a '#' inside a string needs a parser, and a
        // wrong answer is a script that no longer runs. So anything after code
        // on the same line is left exactly as it is.
        const string script = "$hash = '#not-a-comment'   # but this one is";
        Assert.Equal(script, WindowsMetrics.ForDelivery(script));
    }

    [Fact]
    public void DeliveryIsNewlineNormalised()
    {
        // The file is checked out with LF by .gitattributes, but a delivered
        // CR would end up inside a PowerShell statement.
        Assert.Equal("$x = 1\n$y = 2", WindowsMetrics.ForDelivery("$x = 1\r\n$y = 2\r\n"));
        Assert.DoesNotContain('\r', WindowsMetrics.ForDelivery(Probes.WindowsScript));
    }

    [Fact]
    public void TheRealScriptHasNoHereStringForStrippingToMisread()
    {
        // The one construct where a line starting with '#' is content rather
        // than a comment. If this ever fails, the stripping above needs to
        // learn about here-strings before the script can use one.
        Assert.DoesNotContain("@\"", Probes.WindowsScript);
        Assert.DoesNotContain("@'", Probes.WindowsScript);
    }

    [Fact]
    public void StrippingLeavesEveryStatementIntact()
    {
        var delivered = WindowsMetrics.ForDelivery(Probes.WindowsScript);
        var kept = delivered.Split('\n');

        // Nothing empty, nothing that is only a comment, and every line of the
        // original that was neither is still here.
        Assert.DoesNotContain(kept, line => line.Trim().Length == 0);
        Assert.DoesNotContain(kept, line => line.TrimStart().StartsWith('#'));

        var expected = Probes.WindowsScript.Split('\n')
            .Select(line => line.TrimEnd('\r'))
            .Where(line => line.Trim().Length > 0 && !line.TrimStart().StartsWith('#'))
            .ToList();
        Assert.Equal(expected, kept);

        // And it is worth doing: the comments are a quarter of the file, which
        // is the difference between fitting in the command line and not.
        Assert.True(
            delivered.Length < Probes.WindowsScript.Length * 0.8,
            $"stripping saved only {Probes.WindowsScript.Length - delivered.Length} of "
            + $"{Probes.WindowsScript.Length} characters");
    }

    [Fact]
    public void TheDeliveredCommandHasRoomToGrow()
    {
        // Under the limit is the hard requirement (WindowsMetricsTests pins
        // it); this says by how much, so the next probe addition is a
        // judgement rather than a surprise.
        var length = WindowsMetrics.Command.Length;
        Assert.True(length < 7000, $"command is {length} characters, leaving little headroom");
    }
}
