namespace ServerMonitor.Core.Tests;

/// <summary>
/// Reads the shared fixtures (D9) copied next to the test assembly.
/// </summary>
/// <remarks>
/// Real host output lives in <c>shared/fixtures</c> and is read from disk
/// rather than embedded, so a failing assertion can be diffed against the file
/// a human can open. Small synthetic inputs — a two-line <c>/proc/stat</c>
/// pair whose arithmetic a test spells out — stay inline in the test that
/// asserts on them.
/// </remarks>
internal static class Fixture
{
    private static readonly string Root =
        Path.Combine(AppContext.BaseDirectory, "Fixtures");

    public static string Read(string relativePath)
    {
        var path = Path.Combine(Root, relativePath.Replace('/', Path.DirectorySeparatorChar));
        if (!File.Exists(path))
        {
            // Named explicitly: a missing fixture means the csproj's Content
            // glob stopped matching, which otherwise shows up as a parser
            // "returning nothing" and sends you looking in the wrong place.
            throw new FileNotFoundException(
                $"Fixture '{relativePath}' is not next to the test assembly. "
                + $"Looked in {Root}. Is the shared/fixtures Content item still in the csproj?",
                path);
        }
        return File.ReadAllText(path);
    }
}
