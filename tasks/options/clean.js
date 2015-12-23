'use strict';
module.exports = function(grunt) {
  return {
    options: {
      'no-write': true
    },
    dev: {
      src: ["./public/**/*","./public/*"]
    }
  }
};