#!/usr/bin/env ruby

require 'rubygems'
require 'snmp'
require 'yaml'

# Funktion zur ausgabe der help

def show_help
    puts "                                                                   "
    puts "  #{$0} -c <community> -h <Host> [options]                         "
    puts "                                                                   "
    puts "  SNMPSCAN version 1.6.2                                           "
    puts "                                                                   "
    puts "  -h            IP-address or the Hostname of the Targetsystem     "
    puts "  -c            SNMP community                                     "
    puts "  -r            specify the name of displayed Interfaces           "
    puts "  -m            Mark line of selected Interface IP                 "
    puts "  -u            disable additional System-Infos                    "
    puts "                                                                   "
    puts "  --help        display this help and exit                         "
    puts "  --version     output version information and exit                "
    puts "                                                                   "
    exit
end

# Funktion zur ausgabe der version

def show_version
    puts "                                                                   "
    puts "  SNMPSCAN version 1.6.2                                           "
    puts "                                                                   "
end

r_opt = []    #Array fuer reg exp
h_opt = nil   #Host
e_opt = nil   # display Interface errors
c_opt = nil   #Comunity
m_opt = "no"  # default nichts markieren
u_opt = true  # default nichts markieren

reg_ip = Regexp.new '\b(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b'
reg_ipv6 = /^\s*((([0-9A-Fa-f]{1,4}:){7}([0-9A-Fa-f]{1,4}|:))|(([0-9A-Fa-f]{1,4}:){6}(:[0-9A-Fa-f]{1,4}|((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3})|:))|(([0-9A-Fa-f]{1,4}:){5}(((:[0-9A-Fa-f]{1,4}){1,2})|:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3})|:))|(([0-9A-Fa-f]{1,4}:){4}(((:[0-9A-Fa-f]{1,4}){1,3})|((:[0-9A-Fa-f]{1,4})?:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9A-Fa-f]{1,4}:){3}(((:[0-9A-Fa-f]{1,4}){1,4})|((:[0-9A-Fa-f]{1,4}){0,2}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9A-Fa-f]{1,4}:){2}(((:[0-9A-Fa-f]{1,4}){1,5})|((:[0-9A-Fa-f]{1,4}){0,3}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9A-Fa-f]{1,4}:){1}(((:[0-9A-Fa-f]{1,4}){1,6})|((:[0-9A-Fa-f]{1,4}){0,4}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(:(((:[0-9A-Fa-f]{1,4}){1,7})|((:[0-9A-Fa-f]{1,4}){0,5}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:)))(%.+)?\s*$/ 

reg_domain = Regexp.new '^[A-Za-z0-9.-]+\.[A-Za-z]{2,5}$'

ARGV.each_with_index do |option , x |

    # testen der argumente ob version ausgegeben werden soll

    if option == "--version" || option == "-v"
        show_version()
        exit
    end

    # testen der argumente ob help ausgegeben werden soll oder zu wenig argumente

    if option == "--help"
        show_help()
        exit
    end

    #Einlesen der ip option

    if option == "-h"
        if h_opt == nil

            # test ob angegebener Host eine regulaere IP bzw Domian sind. ansonsten ausgabe von help

            if !(ARGV[x+1] =~ reg_ip) && !(ARGV[x+1] =~ reg_domain ) && !(ARGV[x+1] =~ reg_ipv6)
                print "\n Wrong IP format\n"
                show_help()
            else
                if RUBY_VERSION.to_f < 1.9 && ARGV[x+1] =~ reg_ipv6
                    print "\n Your Ruby-Version does not support IPv6\n"
                    show_help()
                else
                    h_opt = ARGV[x+1]
                end
            end
        else
            print "\n Multiple Hosts definated\n"
            show_help()
        end
    end

    #Einlesen der Comunity

    if option == "-c"
        if c_opt == nil
            c_opt = ARGV[x+1]
        else
            print "\n Multiple Comunitys definated\n"
            show_help()
        end
    end

    #array mit req exp fuellen

    if option == "-r"
        r_opt.push  ARGV[x+1]
        ARGV[x+1]=""
    end

    #show errors

    if option == "-e"
        e_opt = true
    end

    #markieren der L3 interfaces

    if option == "-m"
        m_opt="yes"
    end

    #anzeigen der zusatzinfos

    if option == "-u"
        u_opt=false
    end
end

if h_opt == nil
    show_help()
end
if c_opt == nil
    show_help()
end

# definition welche angaben man zum interface will

iftable_columns = ["ifIndex", "ifDescr", "ifHCInOctets", "ifHCOutOctets", "ifInUcastPkts", "ifOutUcastPkts", "ifAlias", "ifInErrors"]

# sauberes beenden beim druecken vom strg+c

trap('INT') do
    print "\e[1G" # an den Anfang der zeile springen um in die erste Spalte zu kommen
    100.times {print "\e[B"} # ans ende vom Terminal springen
    exit
end

puts "\e[39m\e[2J" # standartfarbe setzen und console leeren
print "\e[1;1H\n" # an den Anfang der console springen

#ermitteln der spaltenanzahl der konsole

TIOCGWINSZ = 0x5413
rows, cols = 25, 130
buf = [0, 0, 0, 0].pack("SSSS")
if STDOUT.ioctl(TIOCGWINSZ, buf) >= 0 then
    rows, cols, row_pixels, row_pixels, col_pixels =
        buf.unpack("SSSS")[0..1]
end

devs_config = [ 
    { 
        :name =>     '.*',
        :prio =>     0,
        :cpu_oid =>  "1.3.6.1.2.1.1.4"
    },
    { 
        :name =>     '.*((Huawei)|(H3C)).*',
        :prio =>     1,
        :cpu_oid =>  "1.3.6.1.4.1.2011.2.23.1.18.4.3.1.4"
    },

    { 
        :name =>     '^Juniper Networks.*',
        :prio =>     1,
        :cpu_oid =>  "1.3.6.1.4.1.2636.3.1.13.1.8.9",
        :add_infos => [
            {
                :oid          => "1.3.6.1.4.1.2636.3.1.13.1.11.9.1.0.0",
                :name         => "Memoryusage",
                :type         => "max",
                :relation     => 80
            },
            {
                :oid          => "1.3.6.1.4.1.2636.3.1.13.1.7.9.1.0.0",
                :name         => "Temperatur",
                :type         => "max",
                :relation     => 40
            },
            {
                :oid          => "1.3.6.1.4.1.2636.3.4.2.3.2.0",
                :name         => "Alarm",
                :type         => "same",
                :relation     =>   [
                    {
                        :test    => "0",
                        :output  => "NO",
                        :error   => true
                    },
                    {
                        :test    => "1",
                        :output  => "YES",
                        :error   =>  false
                    }
                ]
            }
        ]
    },

    { 
        :name =>  '^Juniper Networks, Inc. ex8208',
        :prio =>  2,
        :default_filter => [ "^[^.]*$" ]
    },


    { 
        :name =>    '^Juniper Networks, Inc. ex..00-48t.*',
        :prio =>    2,
        :cpu_oid => "1.3.6.1.4.1.2636.3.1.13.1.8.9.1.0",
        :default_filter => [ "[gx]e-0/[012]/[0-9]*$" ],

    },

    { 
        :name =>    '.*Riverstone.*',
        :prio =>    1,
        :cpu_oid => "1.3.6.1.4.1.52.2501.1.270.2.1.1.2",
        :default_filter => [ "Physical port" ],
        :add_infos =>   [
            {
                :oid          => "1.3.6.1.4.1.52.2501.1.1.6.0",
                :name         => "Temperatur",
                :type         => "same",
                :relation     =>   [
                    {
                        :test    => "1",
                        :output  => "normal",
                        :error   => false
                    },
                    {
                        :test    => "2",
                        :output  => "high",
                        :error   => true
                    },
                    {
                        :test    => "3",
                        :output  => "unknown",
                        :error   => true
                    }
                ]
            }
        ]
    },
    { 
        :name =>   '.*3000.*Riverstone.*',
        :prio =>   2,
        :add_infos =>   [
            {
                :oid          => "1.3.6.1.4.1.5567.2.40.1.6.1.1.1.60000001",
                :name         => "Powersupply 1",
                :type         => "same",
                :relation     =>   [
                    {
                        :test    => "noSuchObject",
                        :output  => "ERROR",
                        :error   => true
                    },
                    {
                        :test    => "",
                        :output  => "OK",
                        :error   => false
                    }
                ]
            },
            {
                :oid          => "1.3.6.1.4.1.5567.2.40.1.6.1.1.1.60000002",
                :name         => "Powersupply 2",
                :type         => "same",
                :relation     =>   [
                    {
                        :test    => "noSuchObject",
                        :output  => "ERROR",
                        :error   => true
                    } ,
                    {
                        :test    => "",
                        :output  => "OK",
                        :error   => false
                    }
                ]
            }
        ]
    },
    { 
        :name => '.*8.00.*Riverstone.*',
        :prio => 2,
        :add_infos =>   [
            {
                :oid          => "1.3.6.1.4.1.52.2501.1.1.5.0",
                :name         => "Fan",
                :type         => "same",
                :relation     =>   [
                    {
                        :test    => "1",
                        :output  => "working",
                        :error   => false
                    },
                    {
                        :test    => "2",
                        :output  => "notWorking",
                        :error   => true
                    },
                    {
                        :test    => "3",
                        :output  => "unknown",
                        :error   => true
                    }
                ]
            }
        ]
    }
]

devs_config.sort! do |a , b|
    if a[:prio] == b[:prio] 
        a[:name] <=> b[:name] 
    else 
        a[:prio] <=> b[:prio] 
    end
end





# SNMP connect

begin


    SNMP::Manager.open(:Host => h_opt , :Community => c_opt , :Timeout => 1 , :Retries => 600) do |manager| # aufbau der SNMP connection

        #erkennung das systemtyps und setzen von add_infos

        cpu_oid = ""
        filter = []
        add_infos = []

        systype = manager.get_value("1.3.6.1.2.1.1.1.0")

        devs_config.each do |dev|
            if systype =~ Regexp.new(dev[:name])
                cpu_oid = dev[:cpu_oid] if dev[:cpu_oid]
                filter = filter + dev[:default_filter] if dev[:default_filter] 
                add_infos = add_infos + dev[:add_infos] if dev[:add_infos] 
            end
        end
        # use default filter if no r_opt given
        if r_opt.length == 0
            r_opt =  filter
        end

        if not u_opt
            add_infos =  []
        end

        #markieren der Zeile mit dem interface der abgefragten IP wenn option -m aktiv

        if m_opt == "yes"

            #IPs des Routers mit interfaceID abfragen

            manager.walk(["1.3.6.1.2.1.4.20.1.2 ","1.3.6.1.2.1.4.20.1.1"]) do |row|
                #testen ob interfaceIP = HostIP
                if row[1].value.to_s == h_opt
                    #ermittelte interfaceID speichern
                    m_opt = row[0].value.to_i
                end
            end
        end

        #m-opt enthaelt jetzt die zu markierende InterfaceID

        #alle interfaces anzeigen wenn nichts angegeben

        if r_opt.length == 0
            r_opt = [""]
        end

        #setzen der Variable
        reg_interface = r_opt.map { |r| Regexp.new(r)  }
        old = []                       # Werte vom letzen Durchlauf
        add_oid = []                   # Array fuer die OIDs der weitern abfragen.

        time1 = Time.now.to_i

        #erstdurchlauf des Arrays

        manager.walk(iftable_columns) do |row|
            old[row[0].value.to_i()]=["#{row[0].value}", "#{row[1].value}", "#{row[2].value}", "#{row[3].value}", "#{row[4].value}", "#{row[5].value}", "#{row[6].value}" , "#{row[7].value}"]
        end

        #herausfinden der add_oid

        add_infos.each do |row|
            add_oid << row[:oid]
        end

        #Endlosschleife

        while 1

            print "\e[1;1H\e[K\n" #an consolenanfang springen

            #Ausgabe Systeminformationen incl mehrerer CPUs

            print " System: #{h_opt}       "                                       #ausgabe der SystemIP
            print " Sysname: #{manager.get_value('1.3.6.1.2.1.1.5.0')}       "         #ausgabe des sysnames
            print " CPU: "

            #Ausgabe der CPUlasten

            manager.walk(cpu_oid) do |row|
                row.each do |vb|
                    print "#{vb.value} "  #.rjust(4)
                end
            end

            #enkennen der Zeit seit dem letzten durchlauf und warten wenn geringer als 10

            print "       Reload:"
            time2 = Time.now.to_i
            interval= time2 - time1
            STDOUT.flush
            if interval < 10
                "#{10-interval}".to_i.downto(1) do |count|
                    print "#{count.to_s}  ".rjust(5)
                    print "\e[K"
                    STDOUT.flush
                    sleep(1)
                    print "\e[D\e[D\e[D\e[D\e[D"
                end
                interval= 10
                time2 = Time.now.to_i
            end

            time1 = time2

            print "\n\e[K\n\e[K"

            #Abfrage der add_info

            #Abfrage aller zusaetzlichen Infos, und durchlauf der einzelnen werte

            add_value=manager.get_value(add_oid)

            add_value.each_with_index do |value, index|

                add_info =  add_infos[index]

                #ueberpruefung der abfrage der add_infos


                case add_infos[index][:type]
                when /same/
                    #durchlauf der einzelnen same-typen um die richtigen typen zu finden

                    add_info[:relation].each_with_index do |relation|
                        add_reg = Regexp.new(relation[:test])
                        if  value.to_s =~ add_reg

                            print "\e[31m" if relation[:error] == "1"
                            print " #{add_info[:name]}".ljust(30)
                            print " #{relation[:output]}".ljust(30)
                            print "\e[K\n"
                            print "\e[39m" if relation[:error] == "1"

                        end
                    end

                when /max/

                    #test ob max-wert ueberschritten wurde

                    print "\e[31m" if add_info[:relation].to_i < value.to_i
                    print " #{add_info[:name]}".ljust(30)
                    print " #{value}".ljust(30)
                    print "\e[K\n"
                    print "\e[39m" if add_info[:relation].to_i < value.to_i

                when /min/

                    #test ob min-wert unterschritten wurde

                    print "\e[31m" if add_info[:relation].to_i > value.to_i
                    print " #{add_info[:name]}".ljust(30)
                    print " #{value}".ljust(30)
                    print "\e[K\n"
                    print "\e[39m" if add_info[:relation].to_i > value.to_i

                end
            end

            #ausgabe des Headers
            print "\e[K"
            print "\n ifNr      Port                                Incoming       Outgoing      Packets IN    Packets OUT   Errors    Alias\e[K\n "
            "#{cols-2}".to_i.times{print "-"}
            print "\n\e[K\n"

            STDOUT.flush

            #abfrage der Interface werte

            manager.walk(iftable_columns) do |row|

                #testen ob Interfacebezeichnung auf eine der regexp matched
                match=false
                reg_interface.each do |x|
                    if row[1].value =~ x || row[6].value =~ x
                        match=true
                        break
                    end
                end

                if match

                    #berechnung der werte pro Sekunden

                    if old[row[0].value.to_i()] != nil #ausgabe der werte nur wenn schon alte daten vorhanden sind

                        diffio = (row[2].value.to_i - old[row[0].value.to_i()][2].to_i) / 1024 / 1024 * 8 / interval
                        diffoo = (row[3].value.to_i - old[row[0].value.to_i()][3].to_i) / 1024 / 1024 * 8 / interval #+ (rand()*300).to_i
                        diffip = (row[4].value.to_i - old[row[0].value.to_i()][4].to_i) / interval
                        diffop = (row[5].value.to_i - old[row[0].value.to_i()][5].to_i) / interval
                        diff_err_in = (row[7].value.to_i - old[row[0].value.to_i][7].to_i) / interval

                        #ausgabe der werte und evtl setzen der farbe

                        print "\e[1;30m"  if diffio < 5 && diffoo < 5 && diffip < 100 && diffop < 100 
                        print "\e[1;31m"  if diffio > 700 || diffoo > 700 || diffip > 100000 || diffop > 100000 || ( diff_err_in != 0)
                        print "\e[33m"  if m_opt == row[0].value.to_i

                        print " #{row[0].value}".ljust(11)
                        print "#{row[1].value[0,30]}".ljust(30)
                        print "#{diffio} Mbit/s".rjust(15)
                        print "#{diffoo} Mbit/s".rjust(15)
                        print "#{diffip} pps".rjust(15)
                        print "#{diffop} pps".rjust(15)
                        print "#{diff_err_in} ".rjust(10) 
                        print "   #{row[6].value[0,cols-94]}\e[K"
                        print "\n"

                        print "\e[0;39m" 
                    end
                    #speichern der alten werte
                    old[row[0].value.to_i()]=["#{row[0].value}", "#{row[1].value}", "#{row[2].value}", "#{row[3].value}", "#{row[4].value}", "#{row[5].value}", "#{row[6].value}", "#{row[7].value}"]
                end
            end
            100.times {print "\e[2K\e[B"} # rest vom screen loeschen
        end
    end
rescue SNMP::RequestTimeout

    print "\e[1G" # an den Anfang der zeile springen um in die erste Spalte zu kommen
    100.times {print "\e[B"} # ans ende vom Terminal springen
    puts "Timeout for 600s"
    exit

end
