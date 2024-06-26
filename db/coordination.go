package db

import (
	"context"
	"database/sql"

	"github.com/cyverse-de/data-usage-api/config"
	"github.com/cyverse-de/data-usage-api/natsconn"
	"github.com/cyverse-de/data-usage-api/util"
	"github.com/pkg/errors"

	"go.opentelemetry.io/otel"
)

type BothDatabases struct {
	deconn        DatabaseTxAccessor
	icatconn      DatabaseTxAccessor
	configuration *config.Config
	nc            *natsconn.Connector

	DERollback func()
	DECommit   func() error

	ICATRollback func()
	ICATCommit   func() error

	detx   *DEDatabase
	icattx *ICATDatabase
}

func NewBoth(dedb DatabaseTxAccessor, icatdb DatabaseTxAccessor, config *config.Config, nc *natsconn.Connector) *BothDatabases {
	return &BothDatabases{deconn: dedb, icatconn: icatdb, configuration: config, nc: nc}
}

func (b *BothDatabases) DETx(ctx context.Context) (*DEDatabase, error) {
	logStats("DE", b.deconn)
	if b.detx != nil {
		return b.detx, nil
	}

	detx, err := b.deconn.BeginTxx(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "Error creating DE transaction")
	}

	rb := func() {
		alreadyDoneErr := "sql: transaction has already been committed or rolled back"
		err := detx.Rollback()
		if err != nil && err.Error() != alreadyDoneErr {
			e := errors.Wrap(err, "Error rolling back DE database transaction")
			log.Error(e)
		}
		b.detx = nil
		b.DERollback = func() {}
		b.DECommit = func() error { return nil }
	}

	commit := func() error {
		err := detx.Commit()
		b.detx = nil
		b.DERollback = func() {}
		b.DECommit = func() error { return nil }
		if err != nil {
			e := errors.Wrap(err, "Error committing DE database transaction")
			log.Error(e)
			return e
		}
		return nil
	}

	b.detx = NewDE(detx, b.configuration)
	b.DERollback = rb
	b.DECommit = commit

	return b.detx, nil
}

func (b *BothDatabases) ICATTx(ctx context.Context) (*ICATDatabase, error) {
	logStats("ICAT", b.icatconn)
	if b.icattx != nil {
		return b.icattx, nil
	}

	icattx, err := b.icatconn.BeginTxx(ctx, nil)
	if err != nil {
		return nil, errors.Wrap(err, "Error creating ICAT transaction")
	}

	rb := func() {
		alreadyDoneErr := "sql: transaction has already been committed or rolled back"
		err := icattx.Rollback()
		if err != nil && err.Error() != alreadyDoneErr {
			e := errors.Wrap(err, "Error rolling back ICAT transaction")
			log.Error(e)
		}
		b.icattx = nil
		b.ICATRollback = func() {}
		b.ICATCommit = func() error { return nil }
	}

	commit := func() error {
		err := icattx.Commit()
		b.icattx = nil
		b.ICATRollback = func() {}
		b.ICATCommit = func() error { return nil }
		if err != nil {
			e := errors.Wrap(err, "Error committing ICAT transaction")
			log.Error(e)
			return e
		}
		return nil
	}

	b.icattx = NewICAT(icattx, b.configuration)
	b.ICATRollback = rb
	b.ICATCommit = commit

	return b.icattx, nil
}

func (b *BothDatabases) UpdateUserDataUsage(context context.Context, username string) (*natsconn.UserDataUsage, error) {
	ctx, span := otel.Tracer(otelName).Start(context, "UpdateUserDataUsage")
	defer span.End()

	dedb, err := b.DETx(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "Error creating DE transaction")
	}
	defer b.DERollback()

	icatdb, err := b.ICATTx(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "Error creating ICAT transaction")
	}
	defer b.ICATRollback()

	userInfo, err := dedb.GetUserInfo(ctx, username)
	if err != nil {
		return nil, errors.Wrap(err, "error getting user info")
	}

	usagenum, err := icatdb.UserCurrentDataUsage(ctx, username)
	if err == sql.ErrNoRows {
		usagenum = 0
		log.Infof("No usage information was found for user %s. Attempting to add a usage of 0 anyway", username)
	} else if err != nil {
		return nil, errors.Wrap(err, "Error getting current data usage")
	}
	b.ICATRollback()

	log.Debugf("username %s; usage value %d", username, usagenum)
	// if this update shouldn't be added, or should amend a prior reading, do it here or in the method called below
	// or maybe have an async cleanup process that deduplicates readings

	res, err := b.nc.UpdateUsageForUser(ctx, b.configuration, username, float64(usagenum))
	if err == sql.ErrNoRows {
		e := errors.Wrap(err, "No data could be inserted. Perhaps the user doesn't exist in the DE database")
		log.Error(e)
		return nil, e
	} else if err != nil {
		e := errors.Wrap(err, "Error adding user data usage")
		log.Error(e)
		return nil, e
	}

	res.UserID = userInfo.ID
	res.Username = userInfo.Username

	return res, err
}

func (b *BothDatabases) UpdateUserDataUsageBatch(context context.Context, start, end string) ([]*natsconn.UserDataUsage, error) {
	ctx, span := otel.Tracer(otelName).Start(context, "UpdateUserDataUsageBatch")
	defer span.End()

	// should pass in qualified usernames, icatdb method will strip it as needed
	icatdb, err := b.ICATTx(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "Error creating ICAT transaction")
	}
	defer b.ICATRollback()

	usages, err := icatdb.BatchCurrentDataUsage(ctx, start, end)
	if err != nil {
		return nil, err
	}
	b.ICATRollback()

	log.Tracef("usages in batch: %+v", usages)

	var us []string
	usagesFixed := make(map[string]float64)
	for usr, usg := range usages { // keys of usages map
		us = append(us, util.FixUsername(usr, b.configuration))
		usagesFixed[util.FixUsername(usr, b.configuration)] = float64(usg)
	}

	dedb, err := b.DETx(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "Error creating DE database transaction")
	}
	defer b.DERollback()

	if len(us) > 0 {
		err = dedb.EnsureUsers(ctx, us)
		if err != nil {
			return nil, errors.Wrap(err, "Error ensuring users exist")
		}
	} else {
		log.Tracef("No users to be ensured in the batch")
	}

	err = b.DECommit()
	if err != nil {
		e := errors.Wrap(err, "Error committing DE transaction")
		log.Error(e)
		return nil, e
	}

	res, err := b.nc.AddUserUpdatesBatch(ctx, b.configuration, usagesFixed)
	if err != nil {
		return nil, errors.Wrap(err, "Error inserting new usage")
	}

	return res, nil
}
