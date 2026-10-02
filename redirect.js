// Forward to the web client, preserving the invite fragment. replace() keeps
// the secret-bearing URL out of an extra history entry.
location.replace('web/' + location.search + location.hash);
