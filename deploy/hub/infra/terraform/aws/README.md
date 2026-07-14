# aws/ — future sibling (do NOT build now)

Placeholder per [`docs/design/hub/06-iac.md` §3](../../../../docs/design/hub/06-iac.md).
When a second deploy target justifies it, mirror `azure/`: implement the same
four capability contracts (`../modules/`), swapping the `azurerm` provider for
`aws` and the azurerm state backend for `s3`.

Nothing is built here yet. The stable output-name contract (see `../modules/`)
is what keeps adding `aws/` from becoming a rewrite.
