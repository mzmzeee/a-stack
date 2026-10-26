# Upgrade fixture

`v017.db` is a SQLite database written by the `v0.1.7` tag (`2f332c6`), using its real Store methods and `tasksFixture` test helper. It has synthetic Agents, one assigned Task, an open question, Task mail, standing orders and operation receipts. It contains credential hashes, not credential tokens. No production data is included.

To regenerate, unpack `git archive v0.1.7` outside this checkout. Add a temporary test in its `internal/stack` package which calls `tasksFixture(t)`, then:

1. Create a Task with `f.create` and assign `f.aID` with `f.change`.
2. Call `f.change` with `attention.set` and the question `Which release?`.
3. Call `s.SendMail` with operation ID `fixture-mail-send` and body `Retain this mail.`.
4. Call `s.ChangeOrders` with operation ID `fixture-orders-set`, expected revision 1 and body `Keep the fixture data.`.
5. Write this file with `s.db.Exec("VACUUM INTO ?", destination)`.

Run only that test with `GOTOOLCHAIN=go1.27.3 go test ./internal/stack -run TestGenerateV017UpgradeFixture -count=1`. Random IDs and timestamps will differ. `TestMigration10FromV017` checks every retained row before and after migration and in the schema 9 rollback copy.
