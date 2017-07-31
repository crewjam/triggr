#!/bin/bash
set -e

if [ -z "$GIT_CLONE_URL" ] ; then
    if [ ! -z "$GITHUB_REPO" ] ; then 
        if [ ! -z "$GITHUB_ACCESS_TOKEN" ] ; then
            GIT_CLONE_URL="https://${GITHUB_ACCESS_TOKEN}:@github.com/${GITHUB_REPO}.git"
        else 
            GIT_CLONE_URL="git://github.com/${GITHUB_REPO}.git"
        fi
        SOURCE_DIR=${SOURCE_DIR-$GOPATH/src/github.com/$GITHUB_REPO}
    fi
fi

if [ -z "$SOURCE_DIR" ] ; then
    if [ ! -z "$GITHUB_REPO" ] ; then
        SOURCE_DIR=${SOURCE_DIR-$GOPATH/src/github.com/$GITHUB_REPO}
    fi
    SOURCE_DIR=${SOURCE_DIR-/src}
fi

if [ ! -z "$GIT_CLONE_URL" ] ; then
    (set -x; git clone $GIT_CLONE_URL $SOURCE_DIR)
    if [ ! -z "$GIT_REF" ] ; then
        git -C $SOURCE_DIR fetch origin +$GIT_REF:
        git -C $SOURCE_DIR checkout -qf FETCH_HEAD
    fi
fi

cd $SOURCE_DIR

exec "$@"
