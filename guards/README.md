# Mad API customization lock

`customizations.sha256` is a byte-for-byte lock for the source files that carry
Mad API's accepted custom behavior and visual design.

The release workflow applies every patch and then verifies this manifest before
building. If upstream changes any protected file, or a patch no longer recreates
the accepted result exactly, the workflow must fail and no new release may be
published. The server will therefore keep running the previous known-good image.

Do not refresh these hashes merely to make CI pass. First merge the upstream
change into the customized source, verify every affected behavior, and obtain
explicit acceptance for any visual change. Update the manifest only after that
review is complete.
