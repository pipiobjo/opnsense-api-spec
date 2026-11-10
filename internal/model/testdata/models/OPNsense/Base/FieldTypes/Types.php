<?php
namespace OPNsense\Base\FieldTypes;

abstract class BaseField {}
abstract class BaseListField extends BaseField {}
class BaseSetField extends BaseField {}
class BooleanField extends BaseField {}
class IntegerField extends BaseField {}
class ArrayField extends BaseField {}
class OptionField extends BaseListField {}
class PortField extends BaseListField {}
class InterfaceField extends BaseListField {}
class HostnameField extends BaseSetField {}
class TextField extends BaseField {}
